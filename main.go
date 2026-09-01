// Package main 是 FlowGo 服务的入口，负责组装各层组件并启动 HTTP 服务器。
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/example/flowgo/config"
	"github.com/example/flowgo/controller"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/repository"
	"github.com/example/flowgo/router"
	"github.com/example/flowgo/service"

	"go.uber.org/zap"
)

// main 程序入口：加载配置、初始化日志与数据库、组装依赖、启动 HTTP 服务。
func main() {
	config.LoadConfig()

	if err := config.InitDirectories(); err != nil {
		log.Fatalf("failed to init directories: %v", err)
	}

	if err := logger.Init(config.GetLogPath(), config.GetLogLevel()); err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer logger.Sync()

	db := config.NewDatabase()

	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)
	userController := controller.NewUserController(userService)

	statusService := service.NewStatusService()
	statusController := controller.NewStatusController(statusService)

	r := router.NewRouter(userController, statusController)

	port := config.GetServerPort()
	logger.Info("server starting", zap.Int("port", port))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), r); err != nil {
		logger.Fatal("server exited", zap.Error(err))
	}
}
