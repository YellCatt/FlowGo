// Package config 负责配置文件的加载、数据库连接初始化与日志目录创建。
package config

import (
	"log"
	"path/filepath"
	"time"

	"os"

	"gopkg.in/yaml.v3"
)

// defaultMaxRunDurationMin 未配置或配置非法时，单次运行的最长持续时间（分钟）。
const defaultMaxRunDurationMin = 30

// RunConfig 工作流运行相关的配置。
type RunConfig struct {
	MaxDurationMin int `yaml:"max_duration_min"` // 单次运行的最长持续时间（分钟），超时后整体中止
}

// LLMConfig 大模型配置，为 ai_agent 节点提供默认值。
type LLMConfig struct {
	BaseURL string `yaml:"base_url"` // OpenAI 兼容接口地址
	APIKey  string `yaml:"api_key"`  // 接口密钥
	Model   string `yaml:"model"`    // 默认模型名
	Timeout int    `yaml:"timeout"`  // 单次请求超时（秒）
}

// Config 应用程序的根配置结构，聚合了服务、数据库、日志和大模型配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`   // 服务端配置
	Database DatabaseConfig `yaml:"database"` // 数据库配置
	Log      LogConfig      `yaml:"log"`      // 日志配置
	Run      RunConfig      `yaml:"run"`      // 运行配置（单次运行的超时保护）
	LLM      LLMConfig      `yaml:"llm"`      // 大模型配置（ai_agent 节点默认值）
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
			Port: 9001,
		},
		Database: DatabaseConfig{
			Path: "./data.db",
		},
		Log: LogConfig{
			Path:  "./logs",
			Level: "info",
		},
		Run: RunConfig{
			MaxDurationMin: defaultMaxRunDurationMin,
		},
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "",
			Model:   "gpt-4o-mini",
			Timeout: 60,
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

// GetMaxRunDuration 返回单次运行的最长持续时间，超时后整条流程被中止。
// 未配置或配置为非正数时回退到默认值 30 分钟。
func GetMaxRunDuration() time.Duration {
	if cfg.Run.MaxDurationMin > 0 {
		return time.Duration(cfg.Run.MaxDurationMin) * time.Minute
	}
	return time.Duration(defaultMaxRunDurationMin) * time.Minute
}

// GetLLMBaseURL 返回大模型接口地址，节点未单独配置时使用。
func GetLLMBaseURL() string { return cfg.LLM.BaseURL }

// GetLLMAPIKey 返回大模型接口密钥，节点未单独配置时使用。
func GetLLMAPIKey() string { return cfg.LLM.APIKey }

// GetLLMModel 返回默认大模型名称，节点未单独配置时使用。
func GetLLMModel() string { return cfg.LLM.Model }

// GetLLMTimeout 返回大模型请求默认超时秒数，节点未单独配置时使用。
func GetLLMTimeout() int { return cfg.LLM.Timeout }

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