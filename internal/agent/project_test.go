package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AGENTS.md 注入全链路：FactsFromEnv 读盘 → header 首拍 → resume 重建
// 字节一致；/new 从磁盘重读最新内容（SPEC 6.10）。
func TestProjectInstructionsLifecycle(t *testing.T) {
	f := newFixture(t, &fakeProvider{})
	agentsMD := filepath.Join(f.work, "AGENTS.md")
	if err := os.WriteFile(agentsMD, []byte("Always answer in Chinese.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if facts := FactsFromEnv(f.work); facts.Project != "Always answer in Chinese.\n" {
		t.Errorf("FactsFromEnv 未读入 AGENTS.md: %q", facts.Project)
	}

	// /new：从磁盘重读并首拍进新 header，系统提示词随之含项目指令段。
	sess, err := f.ag.newSession()
	if err != nil {
		t.Fatal(err)
	}
	if sess.Header().AgentsMD != "Always answer in Chinese.\n" {
		t.Errorf("new header AgentsMD = %q", sess.Header().AgentsMD)
	}
	f.ag.switchSession(sess)
	if !strings.Contains(f.ag.system, "Project instructions (from AGENTS.md):\nAlways answer in Chinese.") {
		t.Errorf("系统提示词缺项目指令段:\n%s", f.ag.system)
	}

	// resume 语义：从 header 重建与首拍字节一致（改磁盘不影响老会话）。
	if err := os.WriteFile(agentsMD, []byte("changed on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuilt := BuildSystemPrompt(PromptFacts{
		Cwd: sess.Header().Cwd, OS: sess.Header().OS, Shell: sess.Header().Shell,
		Date: sess.Header().Created[:10], Project: sess.Header().AgentsMD,
	})
	if !strings.Contains(rebuilt, "Always answer in Chinese.") || strings.Contains(rebuilt, "changed on disk") {
		t.Errorf("resume 重建应使用 header 首拍内容:\n%s", rebuilt)
	}
}

// 无 AGENTS.md：Project 为空，提示词与既有 golden 形态一致（旧会话兼容）。
func TestProjectInstructionsAbsent(t *testing.T) {
	if facts := FactsFromEnv(t.TempDir()); facts.Project != "" {
		t.Errorf("无 AGENTS.md 应为空串，got %q", facts.Project)
	}
}
