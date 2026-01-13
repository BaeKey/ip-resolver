package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config 为全局配置结构
type Config struct {
	// Server
	ListenAddr  string `mapstructure:"listen_addr"`
	MonitorAddr string `mapstructure:"monitor_addr"`
	WorkerConcurrency int `mapstructure:"worker_concurrency"`

	// Cache
	CacheTTLSeconds   int64 `mapstructure:"cache_ttl_seconds"`
	CacheRefreshRatio int   `mapstructure:"cache_refresh_ratio"`

	// Provider 配置
	Provider ProviderConfig `mapstructure:"provider"`

	// Quota 配置
	Quota QuotaConfig `mapstructure:"quota"`

	// Log
	LogLevel string `mapstructure:"log_level"`
	LogFile  string `mapstructure:"log_file"`
}

// ProviderConfig 为数据提供方配置
type ProviderConfig struct {
	Name      string `mapstructure:"name"`
	SecretID  string `mapstructure:"secret_id"`
	SecretKey string `mapstructure:"secret_key"`
}

type QuotaConfig struct {
	SecretID   string `mapstructure:"secret_id"`   // 腾讯云官方 AKID
	SecretKey  string `mapstructure:"secret_key"`  // 腾讯云官方 Key
	InstanceID string `mapstructure:"instance_id"` // 资源包 ID
}

// SetDefaults 设置所有配置默认值
func SetDefaults() {
	viper.SetDefault("log_level", "info")

	// Server
	viper.SetDefault("listen_addr", "127.0.0.1:8080")
	viper.SetDefault("monitor_addr", "127.0.0.1:9090")
	viper.SetDefault("worker_concurrency", 8)

	// Cache
	viper.SetDefault("cache_ttl_seconds", int64(30*24*60*60)) // 30 天
	viper.SetDefault("cache_refresh_ratio", 10)
}

// LoadConfig 加载配置文件并反序列化
func LoadConfig(path string) (*Config, error) {
	SetDefaults()

	if path != "" {
		viper.SetConfigFile(path)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	return &cfg, nil
}
