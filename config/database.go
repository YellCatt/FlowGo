package config

import (
	"fmt"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

// NewDatabase 打开 SQLite 连接并自动迁移全部模型。
// 驱动使用 github.com/mattn/go-sqlite3（CGO 版 SQLite3），
// 兼容 MIPS 等 32 位小端平台（如极路由 2 / MT7620），
// 编译时需要 CGO_ENABLED=1 及对应平台的交叉编译工具链。
func NewDatabase() (*gorm.DB, error) {
	logger.Debug("数据库：开始打开 SQLite 连接", zap.String("路径", cfg.Database.Path))

	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{
		Logger: gormlog.Default.LogMode(gormlog.Warn),
	})
	if err != nil {
		logger.Error("数据库：打开连接失败", zap.String("路径", cfg.Database.Path), zap.Error(err))
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	logger.Debug("数据库：连接已建立")

	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		logger.Error("数据库：设置 journal_mode 失败", zap.Error(err))
		return nil, fmt.Errorf("failed to set journal mode: %w", err)
	}
	logger.Debug("数据库：已启用 WAL 日志模式")

	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		logger.Error("数据库：设置 busy_timeout 失败", zap.Error(err))
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}
	logger.Debug("数据库：已设置 busy_timeout=5000")

	logger.Debug("数据库：开始自动迁移表结构", zap.Strings("模型", []string{"Workflow", "Run", "StepLog"}))
	if err := db.AutoMigrate(&model.Workflow{}, &model.Run{}, &model.StepLog{}); err != nil {
		logger.Error("数据库：自动迁移失败", zap.Error(err))
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}
	logger.Info("数据库：表结构自动迁移完成")

	return db, nil
}
