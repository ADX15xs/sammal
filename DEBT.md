# DEBT.md — 显式技术债账本

> 第 6 章 I6：一切妥协显式记账。格式：`| 位置 | 为什么妥协 | 怎么还 | 何时还 |`。
> 代码内 TODO/FIXME 必须关联本账本条目；新增 workaround 未记账视为验收失败。

| 位置 | 为什么妥协 | 怎么还 | 何时还 |
|---|---|---|---|
| internal/tui/input.go | 粘贴多行内容平铺存储，输入框只显示首行 + 行数标注（完整文本仍随 Enter 提交）；多行内联编辑的光标/展示成本高 | 多行输入的主路径是 Ctrl+E 外部编辑器；若内联多行成为实际痛点再做行感知光标 | 按需（原定时点 M3 已过：Ctrl+E 已落地为多行主路径） |
| internal/agent/agent.go | reasoning 增量已落盘（assistant/chunk kind=reasoning）并实时渲染为单行流式（dsh 方案），定稿即从视窗丢弃；DeepSeek 官方端点 thinking mode 要求 tools 请求回传 reasoning_content，sammal 尚不回传——本地端点（Ollama/vLLM/LM Studio）无此要求不受影响 | 接入 DeepSeek 官方端点跑工具环时：provider.Message 加 reasoning 字段 + 按端点开关的序列化回传 | 首个 DeepSeek 官方 thinking 端点接入时 |
| 全项目 | CJK 预组合文本的 输入编辑 / 光标列计算 / 流式渲染 / 日志落盘 已全自动化（uniseg+runewidth 单测、E2E 断言中文回复渲染）。不可自动化的剩余部分：真实 IME 组合输入流程（候选窗、组合态光标）与三大终端产品（Windows Terminal / iTerm2 / Linux 终端）上的视觉效果、原生滚动复制 | 发布前实机冒烟：每端各过一遍「IME 敲中文 → 删除/移动光标 → Esc 中止 → 选区复制」 | 每次发版前（M4 时点已过且无入账证据，转为常设项） |
| internal/tool/bash.go | PowerShell **执行路径**已自动化验证（powershell.exe/pwsh 端到端：输出、非零退出码、超时杀进程，见 TestBashToolPowerShellPath）。剩余未覆盖：本机 System32 存在 WSL bash.exe，exec.LookPath("bash") 恒命中，「PATH 无 bash → 降级」检测分支不可达 | 在无 bash 的 Windows 实机触发一次降级（或该分支由 3 行 fallback 与 prompt golden 互补覆盖，评估可接受） | 首个无 bash 的 Windows 实机出现时 |
| internal/tool/grep.go | rg 委托与纯 Go 后备的正则方言存在细微差异（rg 为 Rust regex 语法，后备为 RE2）；常见模式两者一致 | 遇到实际分歧模式时在 schema description 中声明方言边界，或统一为仅 RE2 | 按需（首个实际分歧出现时） |
| internal/agent/agent.go | 压缩只在 turn 开始触发：单个 turn 内多 step 工具环撑爆上下文时不自动恢复（请求会收到上下文溢出错误，turn 以 error 结束；下一 turn 开始时自动压缩后可重试） | 溢出分类（InterruptContextOverflow）已就绪；实测出现该场景再加「溢出 → turn 内压缩重试」 | 按需（首个实际溢出出现时） |
| internal/provider/chunk.go | 限流信号识别面收敛：头部只认标准 Retry-After（整数秒/HTTP-date）与常见 x-ratelimit-reset（unix 秒/RFC3339），其余变体格式（如 Go duration 串 "6m30s"、各家自定义头）退化为盲指数退避；配额窗口判档依赖响应体英文特征串，非英文报错可能漏判成普通限流 | 遇到实际端点时按其格式扩展 parseRetryAfter / quotaMarkers | 按需（首个未识别格式实际影响重试效果时） |
| internal/agent/agent.go | Usage（含 prompt_cache_hit_tokens / cached_tokens）只透出 TUI 显示当前轮，不落日志：无法从日志回答会话级/跨轮缓存命中率，成功标准 #3 只兑现了实时半边 | 给 assistant/message 或 turn/end 事件补 usage 字段（I1「模型可见=已写入日志」的自然延伸），TUI 改读事件即可 | 按需（需要会话级命中率审计或跨轮指标时） |
| internal/agent/agent.go runSteps | turn 内无 step 数软上限，压缩只在 turn 开始触发：模型失控循环调用工具时会一直烧到上下文溢出才停（大窗口端点损失放大） | 先加软阈值 StatusEvent 提醒；实测出现失控再加溢出后 turn 内压缩重试或硬上限 | 按需（首个实际失控出现时） |
| internal/agent/agent.go captureBeforeWrite | 快照捕获按 `{path}` 参数启发式识别写类工具：未来新增参数不含 path 的写工具会被静默漏快照（/rewind 盲区扩大且无告警） | Tool 接口加显式声明（如 SnapshotTargets 方法）替代启发式解析 | 首个非 path 参数的写工具进入注册表时 |
| internal/tool/tool_test.go | bash/grep 测试用真实 sleep 脚本化并发行为，整包 ~32s：拖慢开发反馈环，不影响正确性 | 用 channel 同步或轮询断言替代定时 sleep | 下次大改 tool 包时顺手 |
| internal/session/assets.go | 图片字节存会话目录 assets/（内容寻址）而非日志本身：资产文件被外部删除、或旧格式日志（Images 存绝对路径）时该图跳过、对应请求重放哈希不一致（既定降级语义，TestReplayRequestHashesWithImages 锁定）。设计保证：图片不进投影 → 跨轮切换模型不受多模态限制；turn 内端点由 switchModel 运行中守卫恒定 | 无可还——字节不在即不可还原，属物理边界而非设计缺口 | 无（边界说明） |
| internal/agent/agent.go Submit | `go a.Run` 与 goroutine 内 `setRunning` 之间有微秒级窗口，`Running()` 尚为 false 时 switchModel/idleGuard 命令理论上可插入（对 /new 等命令同样存在的既有竞态，与图片无关）；TUI 人手操作（≥ 数百毫秒）实际不可达 | 把运行标记提前到 Submit 同步段（pending 状态位），或 Run 启动前二次校验 | 按需（出现程序化紧邻调用 Submit+Slash 的用法时） |
| internal/compaction/compaction.go | 压缩估算不计图片 token（EstimateRequest/eventTokens 只计文本 part）：近满窗口的带图请求可能真实溢出（图片 base64 单张可达 ~27MB） | 图片字节折算 token（如 4 字节/token）或附加固定配额计入估算 | 按需（首个实际溢出出现时） |
| internal/agent/agent.go Submit | 生成中提交的图片被静默丢弃：插话收件箱（Steering）只承载文本 | 收件箱扩展图片负载，或 TUI 在生成中带图提交时明确提示 | 按需（生成中带图插入成为实际用法时） |
