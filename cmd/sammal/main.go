// Sammal 薄入口：解析参数、装配、启动（第 5.1 节）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/config"
	"sammal/internal/provider"
	"sammal/internal/tui"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径（默认 ~/.config/sammal/config.toml）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	modelName, modelCfg, err := cfg.Resolve("")
	if err != nil {
		fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	ag := agent.New(rootCtx, provider.NewClient(modelCfg.BaseURL, os.Getenv(modelCfg.APIKeyEnv)),
		modelCfg.Model, agent.BuildSystemPrompt(agent.FactsFromEnv(cwd)))

	p := tea.NewProgram(tui.New(tui.Deps{
		ModelName: modelName,
		Events:    ag.Events(),
		Send:      ag.Submit,
		Abort:     ag.Abort,
	}))
	if _, err := p.Run(); err != nil {
		fatal(fmt.Errorf("启动 TUI 失败：%w", err))
	}
	cancelRoot()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sammal:", err)
	os.Exit(1)
}
