package config

import (
	"fmt"

	"github.com/example/flowgo/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewDatabase 打开 SQLite 连接并自动迁移全部模型。
// 驱动使用纯 Go 实现（modernc.org/sqlite），无需 CGO，可交叉编译为单文件。
func NewDatabase() (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, fmt.Errorf("failed to set journal mode: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	if err := db.AutoMigrate(&model.Workflow{}, &model.Run{}, &model.StepLog{}); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}