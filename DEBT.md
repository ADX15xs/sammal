# DEBT.md — 显式技术债账本

> 第 6 章 I6：一切妥协显式记账。格式：`| 位置 | 为什么妥协 | 怎么还 | 何时还 |`。
> 代码内 TODO/FIXME 必须关联本账本条目；新增 workaround 未记账视为验收失败。

| 位置 | 为什么妥协 | 怎么还 | 何时还 |
|---|---|---|---|
| internal/tui/input.go | 粘贴多行内容平铺存储，输入框只显示首行 + 行数标注（完整文本仍随 Enter 提交）；多行内联编辑的光标/展示成本高 | 多行输入的主路径是 Ctrl+E 外部编辑器；若内联多行成为实际痛点再做行感知光标 | M3（Ctrl+E 落地后复评） |
| internal/agent/agent.go | reasoning 增量仅实时推给 TUI，不进历史与日志；resume 后思考过程不可回放（OpenAI 协议本身不回传思考，回放价值低） | 若确需回放思考，给 assistant/chunk 事件加 kind 字段并落日志 | 按需（出现真实回放需求时） |
| 全项目 | M0 验收要求 Windows Terminal / iTerm2 / Linux 终端三端人工过 CJK 输入与流式渲染；自动化只能覆盖 headless 链路与纯函数逻辑 | 实机人工验收清单：CJK 输入/删除/光标、Esc 中止、滚动区复制 | M4 发布前 |
| internal/tool/bash.go | PowerShell 降级路径（无 bash 的 Windows）在本开发机无法自动验证（本机 PATH 有 bash）；单测只覆盖 bash/sh 路径 | 在无 bash 的 Windows 实机运行工具环验收清单（M1 验收项） | M4 发布前 |
| internal/tool/grep.go | rg 委托与纯 Go 后备的正则方言存在细微差异（rg 为 Rust regex 语法，后备为 RE2）；常见模式两者一致 | 遇到实际分歧模式时在 schema description 中声明方言边界，或统一为仅 RE2 | 按需（首个实际分歧出现时） |
| internal/agent/agent.go | 压缩只在 turn 开始触发：单个 turn 内多 step 工具环撑爆上下文时不自动恢复（请求会收到上下文溢出错误，turn 以 error 结束；下一 turn 开始时自动压缩后可重试） | 溢出分类（InterruptContextOverflow）已就绪；实测出现该场景再加「溢出 → turn 内压缩重试」 | 按需（首个实际溢出出现时） |
