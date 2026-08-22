package config

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvFile 是与 config.toml 同目录的 dotenv 风格 secrets 文件
// （Windows: %APPDATA%\sammal\.env）。解析为键值表后供 api_key_env
// 兜底；同名进程环境变量始终优先（dotenv 惯例，CI/测试注入不被覆盖）。
func EnvFile(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), ".env")
}

// LoadEnvFile 读取 configPath 同目录的 .env；文件不存在时返回空表
// （不报错——secrets 是可选能力）。
func LoadEnvFile(configPath string) map[string]string {
	data, err := os.ReadFile(EnvFile(configPath))
	if err != nil {
		return nil
	}
	return ParseEnv(data)
}

// ParseEnv 解析 dotenv 文本：KEY=VALUE，支持 CRLF、双/单引号、
// 整行 # 注释与空行。不支持的语法（export 前缀、行内注释、
// 多行值）按字面处理——需求只有一行一个密钥。
func ParseEnv(data []byte) map[string]string {
	out := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// ResolveSecret 解析 secrets：进程环境变量优先，.env 表兜底。
func ResolveSecret(name string, envFile map[string]string) string {
	if name == "" {
		return ""
	}
	if v := os.Getenv(name); v != "" {
		return v
	}
	return envFile[name]
}
