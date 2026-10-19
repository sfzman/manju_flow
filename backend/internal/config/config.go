package config

import (
	"encoding/json"
	"manju-flow/utils"
	"strconv"
	"strings"
	"time"
)

var Cfg *Config

// Config 应用配置
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	OSS           OSSConfig
	TTS           TTSConfig
	Ark           ArkConfig
	Wan           WanConfig
	ArkAgentPlan  ArkAgentPlanConfig
	ChapterImport ChapterImportConfig
	App           AppConfig
	CORS          CORSConfig
	Auth          AuthConfig
}

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowOrigins []string
}

// AuthConfig 认证配置
type AuthConfig struct {
	RegisterWhitelist map[string]bool
	// SessionTTL 登录会话有效期，默认 30 天
	SessionTTL time.Duration
}

// AppConfig 应用程序配置
type AppConfig struct {
	Name        string
	Environment string
}

// OSSConfig 阿里云OSS配置
type OSSConfig struct {
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	BucketName      string
}

// TTSConfig 音频合成服务配置
type TTSConfig struct {
	APIURL string
	APIKey string
	Model  string
	Format string
}

// ArkConfig 火山引擎内容生成服务配置（Seedance 视频生成）
type ArkConfig struct {
	APIBaseURL string
	APIKey     string
}

// WanConfig 阿里云百炼万相视频生成配置
type WanConfig struct {
	APIBaseURL  string
	APIKey      string
	WorkspaceID string
	Region      string
}

// ArkAgentPlanConfig 火山引擎 Agent Plan 配置（LLM / 图片理解）
type ArkAgentPlanConfig struct {
	APIBaseURL         string
	APIKey             string
	SupportedLLMModels []string
}

// ChapterImportConfig 章节 txt 导入配置
type ChapterImportConfig struct {
	// AITimeout 单个导入任务 AI 分析的 HTTP 超时；脚本越大越可能需要调大
	AITimeout time.Duration
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port string
	Mode string // debug, release, test
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver   string // mysql, sqlite
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// Load 加载配置
func Load() *Config {
	Cfg = &Config{
		App: AppConfig{
			Name:        "manju",
			Environment: utils.GetEnv("APP_ENV", "local"),
		},
		Server: ServerConfig{
			Port: utils.GetEnv("SERVER_PORT", "8080"),
			Mode: utils.GetEnv("GIN_MODE", "debug"),
		},
		Database: DatabaseConfig{
			Driver:   utils.GetEnv("DB_DRIVER", "sqlite"),
			Host:     utils.GetEnv("DB_HOST", "localhost"),
			Port:     utils.GetEnv("DB_PORT", "3306"),
			User:     utils.GetEnv("DB_USER", "root"),
			Password: utils.GetEnv("DB_PASSWORD", ""),
			DBName:   utils.GetEnv("DB_NAME", "manju_flow"),
			SSLMode:  utils.GetEnv("DB_SSL_MODE", "disable"),
		},
		OSS: OSSConfig{
			Endpoint:        utils.GetEnv("OSS_ENDPOINT", ""),
			AccessKeyID:     utils.GetEnv("OSS_ACCESS_KEY_ID", ""),
			AccessKeySecret: utils.GetEnv("OSS_ACCESS_KEY_SECRET", ""),
			BucketName:      utils.GetEnv("OSS_BUCKET_NAME", ""),
		},
		TTS: TTSConfig{
			APIURL: utils.GetEnv("TTS_API_URL", "https://openspeech.bytedance.com/api/v3/tts/create"),
			APIKey: utils.GetEnv("TTS_API_KEY", ""),
			Model:  utils.GetEnv("TTS_MODEL", "seed-audio-1.0"),
			Format: utils.GetEnv("TTS_AUDIO_FORMAT", "mp3"),
		},
		Ark: ArkConfig{
			APIBaseURL: utils.GetEnv("ARK_API_BASE_URL", "https://ark.cn-beijing.volces.com/api"),
			APIKey:     utils.GetEnv("ARK_API_KEY", ""),
		},
		Wan: WanConfig{
			APIBaseURL:  utils.GetEnv("WAN_API_BASE_URL", ""),
			APIKey:      utils.GetEnv("WAN_API_KEY", ""),
			WorkspaceID: utils.GetEnv("WAN_WORKSPACE_ID", ""),
			Region:      utils.GetEnv("WAN_REGION", "cn-beijing"),
		},
		ArkAgentPlan: ArkAgentPlanConfig{
			APIBaseURL:         utils.GetEnv("ARK_AGENT_PLAN_API_BASE_URL", "https://ark.cn-beijing.volces.com/api/plan"),
			APIKey:             utils.GetEnv("ARK_AGENT_PLAN_API_KEY", ""),
			SupportedLLMModels: parseStringList(utils.GetEnv("ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS", "doubao-seed-2.1-lite")),
		},
		ChapterImport: ChapterImportConfig{
			AITimeout: parseTimeoutSeconds(utils.GetEnv("CHAPTER_IMPORT_AI_TIMEOUT", ""), 5*time.Minute),
		},
		CORS: CORSConfig{
			AllowOrigins: parseOrigins(utils.GetEnv("CORS_ORIGINS", "*")),
		},
		Auth: AuthConfig{
			RegisterWhitelist: parseWhitelist(utils.GetEnv("REGISTER_WHITELIST", "")),
			SessionTTL:        parseSessionTTL(utils.GetEnv("SESSION_TTL", "")),
		},
	}
	return Cfg
}

// parseOrigins 解析允许的域名列表
// 支持逗号分隔: "https://example.com,https://app.example.com"
// 默认 "*" 表示允许所有
func parseOrigins(origins string) []string {
	if origins == "" || origins == "*" {
		return []string{"*"}
	}
	parts := strings.Split(origins, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{"*"}
	}
	return result
}

func parseIntEnv(key string, defaultValue int) int {
	raw := strings.TrimSpace(utils.GetEnv(key, ""))
	if raw == "" {
		return defaultValue
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}

	return parsed
}

func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			result := make([]string, 0, len(values))
			for _, value := range values {
				if item := strings.TrimSpace(value); item != "" {
					result = append(result, item)
				}
			}
			return result
		}
	}

	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

// parseWhitelist 解析注册白名单
// 支持逗号分隔: "user1@example.com,user2@example.com"
func parseWhitelist(whitelist string) map[string]bool {
	result := make(map[string]bool)
	if whitelist == "" {
		return result
	}
	parts := strings.Split(whitelist, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result[p] = true
		}
	}
	return result
}

// parseSessionTTL 解析登录会话有效期
// 支持 Go duration 格式（如 "720h"），纯数字按小时处理；为空或非法时回退默认 30 天
func parseSessionTTL(raw string) time.Duration {
	const defaultTTL = 30 * 24 * time.Hour
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultTTL
	}
	// 纯数字按小时数处理，如 "168" = 168h
	if hours, err := strconv.Atoi(raw); err == nil {
		if hours > 0 {
			return time.Duration(hours) * time.Hour
		}
		return defaultTTL
	}
	if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
		return ttl
	}
	return defaultTTL
}

// parseTimeoutSeconds 解析请求超时
// 支持 Go duration 格式（如 "90s"、"5m"），纯数字按秒处理；为空或非法时回退默认值
func parseTimeoutSeconds(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}
