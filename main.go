// Package main 是 FlowGo 服务的入口，负责组装各层组件并启动 HTTP 服务器。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/flowgo/config"
	"github.com/example/flowgo/controller"
	"github.com/example/flowgo/engine"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/repository"
	"github.com/example/flowgo/router"
	"github.com/example/flowgo/scheduler"
	"github.com/example/flowgo/service"

	_ "github.com/example/flowgo/node" // 注册内置节点：http / shell / delay / ai_agent
	"go.uber.org/zap"
)

var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

func Version() string {
	return version
}

func BuildInfo() string {
	s := version
	if commit != "" {
		s += " (" + commit + ")"
	}
	if buildTime != "" {
		s += " built at " + buildTime
	}
	return s
}

// shutdownTimeout 优雅退出时等待在途请求的最长时间。
const shutdownTimeout = 10 * time.Second

// main 程序入口：加载配置、初始化日志与数据库、组装依赖、启动调度器与 HTTP 服务。
func main() {
	config.LoadConfig()

	if err := config.InitDirectories(); err != nil {
		log.Fatalf("failed to init directories: %v", err)
	}

	if err := logger.Init(config.GetLogPath(), config.GetLogLevel()); err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer logger.Sync()

	db, err := config.NewDatabase()
	if err != nil {
		logger.Fatal("failed to init database", zap.Error(err))
	}

	workflowRepo := repository.NewWorkflowRepository(db)
	runRepo := repository.NewRunRepository(db)

	eng := engine.NewEngine(runRepo)

	workflowService := service.NewWorkflowService(workflowRepo, eng)
	runService := service.NewRunService(runRepo)

	sched := scheduler.NewScheduler(workflowRepo, eng)
	if err := sched.Start(); err != nil {
		logger.Fatal("failed to start scheduler", zap.Error(err))
	}

	workflowController := controller.NewWorkflowController(workflowService, sched)
	runController := controller.NewRunController(runService)
	webhookController := controller.NewWebhookController(workflowService)
	statusController := controller.NewStatusController(service.NewStatusService())

	r := router.NewRouter(version, workflowController, runController, webhookController, statusController, sched)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", config.GetServerPort()),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting",
			zap.String("version", BuildInfo()),
			zap.Int("port", config.GetServerPort()),
			zap.String("console", fmt.Sprintf("http://localhost:%d", config.GetServerPort())),
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// 等待退出信号或服务异常退出。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	case err := <-serverErr:
		if err != nil {
			logger.Error("server exited unexpectedly", zap.Error(err))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	stopped := sched.Stop()
	<-stopped.Done()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server gracefully", zap.Error(err))
	}
	logger.Info("server stopped")
}