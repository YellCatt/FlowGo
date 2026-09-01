package config

import (
	"log"

	"github.com/example/flowgo/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// NewDatabase 根据配置创建并返回一个 SQLite 数据库连接，同时自动迁移 User 模型。
func NewDatabase() *gorm.DB {
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	err = db.AutoMigrate(&model.User{})
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	return db
}
