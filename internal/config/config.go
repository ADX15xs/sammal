// Package config 加载 config.toml：默认值 → 用户配置（第 7.2 节）。
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultContextWindow 未配置时的兜底窗口；compaction 触发阈值依赖它。
const DefaultContextWindow = 32768

type Model struct {
	BaseURL       string `toml:"base_url"`
	Model         string `toml:"model"`
	APIKeyEnv     string `toml:"api_key_env"`
	ContextWindow int    `toml:"context_window"`
}

type UI struct {
	Editor string `toml:"editor"`
}

type Config struct {
	DefaultModel string           `toml:"default_model"`
	Models       map[string]Model `toml:"models"`
	UI           UI               `toml:"ui"`
}

// DefaultPath 返回系统默认配置路径（~/.config/sammal/config.toml，
// Windows 为 %APPDATA%\sammal\config.toml）。
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sammal", "config.toml"), nil
}

// ResolvePath 解析配置路径参数：空串 = 系统默认路径。config.toml 与
// .env 的目录解析必须都经由它，避免空串直接进入 EnvFile 变成相对
// 当前工作目录的地址。
func ResolvePath(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return DefaultPath()
}

// Load 从 path 读取配置；path 为空时使用系统默认路径。
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败（%w）；首次使用请参照 README.md 创建配置", path, err)
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败：%w", path, err)
	}
	for name, m := range c.Models {
		if m.ContextWindow == 0 {
			m.ContextWindow = DefaultContextWindow
			c.Models[name] = m
		}
	}
	return &c, nil
}

// Resolve 返回名为 name 的模型配置；name 为空时返回默认模型。
func (c *Config) Resolve(name string) (string, Model, error) {
	if name == "" {
		name = c.DefaultModel
	}
	m, ok := c.Models[name]
	if !ok {
		return "", Model{}, fmt.Errorf("未定义的模型 %q（配置中共有 %d 个模型）", name, len(c.Models))
	}
	return name, m, nil
}
