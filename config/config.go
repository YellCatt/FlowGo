// Package config 负责配置文件的加载、数据库连接初始化与日志目录创建。
package config

import (
	"log"
	"path/filepath"

	"os"

	"gopkg.in/yaml.v3"
)

// Config 应用程序的根配置结构，聚合了服务、数据库和日志配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`   // 服务端配置
	Database DatabaseConfig `yaml:"database"` // 数据库配置
	Log      LogConfig      `yaml:"log"`      // 日志配置
}

// ServerConfig 服务端相关配置，包括监听端口。
type ServerConfig struct {
	Port int `yaml:"port"` // 服务监听端口
}

// DatabaseConfig 数据库配置，包括数据库文件路径。
type DatabaseConfig struct {
	Path string `yaml:"path"` // 数据库文件路径
}

// LogConfig 日志配置，包括日志目录和日志级别。
type LogConfig struct {
	Path  string `yaml:"path"`  // 日志文件存放目录
	Level string `yaml:"level"` // 日志输出级别（debug/info/warn/error）
}

var cfg Config // 全局配置实例，由 LoadConfig 加载

// LoadConfig 从 config/config.yaml 加载配置文件。
// 如果配置文件不存在，则自动创建默认配置文件。
func LoadConfig() {
	configPath := "config/config.yaml"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Println("config file not found, creating default config...")
		if err := createDefaultConfig(configPath); err != nil {
			log.Fatalf("failed to create default config: %v", err)
		}
	}

	file, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("failed to read config file: %v", err)
	}

	err = yaml.Unmarshal(file, &cfg)
	if err != nil {
		log.Fatalf("failed to parse config file: %v", err)
	}
}

// createDefaultConfig 创建默认配置文件并写入指定路径。
func createDefaultConfig(path string) error {
	defaultCfg := Config{
		Server: ServerConfig{
			Port: 8084,
		},
		Database: DatabaseConfig{
			Path: "./data.db",
		},
		Log: LogConfig{
			Path:  "./logs",
			Level: "info",
		},
	}

	data, err := yaml.Marshal(&defaultCfg)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// GetServerPort 返回当前服务配置的监听端口号。
func GetServerPort() int {
	return cfg.Server.Port
}

// GetDatabasePath 返回数据库文件的存储路径。
func GetDatabasePath() string {
	return cfg.Database.Path
}

// GetLogPath 返回日志文件的存储目录。
func GetLogPath() string {
	return cfg.Log.Path
}

// GetLogLevel 返回当前配置的日志级别。
func GetLogLevel() string {
	return cfg.Log.Level
}

// InitDirectories 初始化所需的日志目录和数据库目录。
func InitDirectories() error {
	if err := os.MkdirAll(cfg.Log.Path, 0755); err != nil {
		return err
	}

	dbDir := filepath.Dir(cfg.Database.Path)
	if dbDir != "." && dbDir != "" {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return err
		}
	}

	return nil
}
