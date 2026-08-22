// Sammal 薄入口：解析参数、装配、启动（第 5.1 节）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"sammal/internal/agent"
	"sammal/internal/checkpoint"
	"sammal/internal/config"
	"sammal/internal/provider"
	"sammal/internal/session"
	"sammal/internal/tool"
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
	facts := agent.FactsFromEnv(cwd)
	root, err := session.DataRoot()
	if err != nil {
		fatal(fmt.Errorf("定位数据目录失败：%w", err))
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	sess, err := session.Create(root, session.Header{
		ID:      session.NewID(),
		Cwd:     cwd,
		Model:   modelCfg.Model,
		Created: facts.Date + "T00:00:00Z",
		OS:      facts.OS,
		Shell:   facts.Shell,
	})
	if err != nil {
		fatal(fmt.Errorf("创建会话失败：%w", err))
	}
	defer sess.Close()

	registry := tool.NewRegistry(
		&tool.ReadTool{WorkDir: cwd},
		&tool.WriteTool{WorkDir: cwd},
		&tool.EditTool{WorkDir: cwd},
		&tool.BashTool{WorkDir: cwd, Shell: facts.Shell},
		&tool.GrepTool{WorkDir: cwd},
		&tool.GlobTool{WorkDir: cwd},
	)

	ag := agent.New(agent.Config{
		Root:        rootCtx,
		Provider:    provider.NewClient(modelCfg.BaseURL, os.Getenv(modelCfg.APIKeyEnv)),
		Session:     sess,
		Registry:    registry,
		Checkpoints: checkpoint.New(sess.Dir(), cwd),
		System:      agent.BuildSystemPrompt(facts),
	})

	p := tea.NewProgram(tui.New(tui.Deps{
		ModelName: modelName,
		Events:    ag.Events(),
		Send:      ag.Submit,
		Abort:     ag.Abort,
		Slash:     ag.Slash,
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
