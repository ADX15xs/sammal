# Changelog

> **编纂规则（2026-11-14 文档治理起生效）**：本文件是**发布时点产物**——每次 tag 发布时从 git log 与 [docs/DECISIONS.md](docs/DECISIONS.md) 摘编一次，日常不实时维护；实时台账是 [docs/DECISIONS.md](docs/DECISIONS.md)（设计裁决）与 [DEBT.md](DEBT.md)（债与未完成事项）。发布动作：打 annotated tag `vX.Y.Z` 并推送 → goreleaser 六目标构建（`.goreleaser.yaml`，tag 经 `-ldflags` 注入 `main.version`；其 GitHub release notes 过滤 `docs:` / `test:` / `chore:` 前缀提交，本文件不受该过滤、保留全维度历史）。下方 `Unreleased` 一节收录上次发布后的变更，下次发布时并入对应版本节。

## Unreleased

> 2026-11-14 之后的变更（下次 tag 发布时编入版本节）。

### Added — 发布 CI 化（2026-11-14）

- 新增 `.github/workflows/release.yml`：push tag `v*` 触发 ubuntu 全量测试 → goreleaser-action 出六目标 GitHub Release；`GITHUB_TOKEN` 走 CI 注入，本地零配置。范式取自 llm-eyes-mcp `publish.yml`，build/release 段升级为 goreleaser-action 保留六目标与 changelog 过滤；不照搬其 `prerelease: true`。副产品：每次 tag 自动产出 Linux 全量回归补证（此前 DEBT 登记的 Linux 复跑条件触发项，其证据自此由 CI 供给）。

## 1.0.0 (2026-11-14) — 首个正式版：M0–M5 全量交付（tag v1.0.0）

> sammal 首个 tag。此前全部 101 个提交（2026-08-22 基线 → 2026-11-13）并入本节；里程碑条目按 `docs/SPEC.md` 第 9 章，缺陷修复以 [docs/SMOKE.md](docs/SMOKE.md) 与 [DEBT.md](DEBT.md) 的核销记录为证。

### Added — 里程碑交付（M0–M4，2026-08-23 一日四连）

- **M0 走路骨架**（`02fb687`）：provider SSE 流式 + 内联 TUI + 单轮对话。
- **M1 工具环与快照**（`80e4526`）：read / write / edit / bash / grep / glob 六件套 + 多 step 循环 + JSONL 日志 + `/rewind`。
- **M2 会话全套**（`e8ba2f6`）：崩溃恢复 + `/new` `/resume` `/branch` + compaction 全配方（0.8× 触发 → 工具输出剪枝 → 结构化摘要 → 0.16× 原文尾部，摘要请求逐字重放前缀复用 KV 缓存）。
- **M3 模型切换与输入打磨**（`c1e01e1`）：`Ctrl+P` 模型选择器 + `Ctrl+E` 外部编辑器 + 切换语义（历史完整保留，KV 缓存如实重建）。
- **M4 发布就绪**（`d470f76`）：goreleaser 六目标（Windows / macOS / Linux × amd64 / arm64）+ E2E + 文档对齐 + DEBT 审计。

### Added — M4 后特性浪潮（2026-08-24 → 11-13）

- **断流三档分类与限流应对**（`18678ff`，2026-08-24）：429 / 5xx 纳入 step 边界指数退避重连，配额窗口快速失败并报恢复时间点；两段式退避与重试上限内置化（`12f6dec`，2026-08-29，见 Changed）。
- **思考链单行流式渲染**（`2ccf906` + 尾部跟随 `aebe5bd`，2026-08-26 → 08-29）：计时 + 最新 token，超宽保留尾部不丢新内容；reasoning 落日志、不进模型投影。
- **多模态图片输入**（`6682349` / `5c1b1bd`，2026-08-28）：`/attach` 附加图片随消息提交；字节按内容寻址落会话 `assets/`，日志只存引用；图片只进请求尾部，前缀与无图会话逐字节一致，重放可完整还原。
- **AGENTS.md 注入与 /skill prompt 简化器**（`b25044d`，2026-08-29）：项目指令会话首拍定格进系统提示词；skill 正文按需展开骑 user turn 尾部——明确调用优于自动触发，不做模型自决的技能加载。
- **回合耗时落款与「今天」结构化落盘**（`8c4dbbc` 2026-08-30；`2c47a4f` 2026-09-02）：回答末尾追加暗色「（耗时 …）」覆盖工具环全程；日期随消息以独立字段落盘、投影时展开为消息头部前缀（系统提示词保持纯静态，前缀缓存全程命中）；`internal/human` 统一时长格式单一真相。
- **滚动缓冲区渐进落盘**（`6e04ae0`，2026-09-05）：assistant 回答闭合行超触发线即落盘 + 定稿补余——长回答不再击穿 insertAbove 视口不变式，流式期间即可搜索复制；宽度归一化 + 巨行硬拆是 bubbletea v2.0.9 两个上游缺陷的应用侧对策（与估算公式实现解耦，上游修复后零改动受益）。
- **Usage 落日志**（`e5c967e`）：`assistant/message` 与 `turn/end` 携带 per-step usage（Prompt / Completion / CachedTokens），会话级与跨轮缓存命中审计补满。
- **多行内联编辑器**（`422f3d0`，2026-09-15）：字素级光标 + 大粘贴折叠 + 垂直窗口，替代单行输入。
- **bash 超时与后台任务立即返回**（`2d9fd2e`，2026-10-23）：`WaitDelay` 强关孤儿进程持有的输出管道，工具调用不再被后台进程拖到管道关闭。
- **I7「非请求不输出」不变量 + 文档契约测试**（2026-11-02 立项，`4f9a1ec` / `2a7d8d6`）：不变量补齐至七条（产品行为约束：除请求触发的回答与工具环直接结果外不自发输出）；`internal/specdoc` 把不变量↔测试锚点、禁 alt-screen、禁裸 TODO、文档交叉引用、800 行棘轮全部机器把守。
- **M5 后台作业全量**（2026-11-05 → 11-11）：JobRegistry、bash `run_in_background`（前台超时晋升不杀）、`job_output` / `job_kill`、`/jobs` 人侧直杀、bg 徽标、退出警告（`26924ce` / `9dc2988` / `00cb04b`）；`job/completed` 结算按日志序归属下一条用户消息尾部注入（`9dc2988`），结算即时通知滚动区与 `/resume` 转录收敛（`8f50e8d`，M6 第 2 项）。
- **bash shell 可配置**（`d49868c` / `09367eb`，2026-11-10）：顶层 `shell` 键 + `ResolveShell` 归一 + 启动时校验（配置错误即时报错而非留到工具调用）；**否决内置 niubash**（约 16MB 载荷 / 携带 GPLv2 的 niu-git 再分发义务 / 未签名触发 SmartScreen，见 `docs/DECISIONS.md` 2026-11-10）。

### Changed

- **Windows 默认 shell 改为 PowerShell**（`09367eb`，2026-11-10 用户裁决）：`DetectShell` 恒返回 powershell（优先 pwsh），「PATH 中 bash 优先」废止；要用 bash 须显式配置 `shell = "bash"`。进程边缘的 shell 环境不可控（PATH 顺序、WSL launcher），显式默认价值高于自动探测。
- **断流重连参数全部内置化**（`12f6dec`，2026-08-29）：移除 `retry_max` 配置键（旧配置键被忽略），次数与退避曲线内置；两段式退避把分钟级限流窗口总耐心提升到约 131s。
- **会话头部 model 字段改存配置键**（`76d9372`，2026-08-27）：同名端点 model 被多个配置复用时不再误判当前模型；`request/header` 仍存端点 model（I1 重放不受影响）。
- **思考行下沉保留 1 行**（`76ef391`，2026-11-04）：reasoning 结束不再整行撤出，改为当前显示行下沉进滚动区 + 原位置保留 1 行暗色摘要（滚动区空洞消失、落盘增量恒 1 行、`/resume` 不含思考）。
- **插话投递语义重构**（2026-11-04）：删除 500ms 去重（`db40684`）——投递层忠实投递，重复与否交由模型权衡；同批 ≥2 条插话合并为一条带抬头与序号的 user 消息（`082aa7c`），不折叠同类项。

### Fixed — 渲染三场硬仗

- **IME 行中插字显示失步**（`b49cccb`，2026-11-02）：Windows Terminal 组合态期间终端自行改写行单元，哈希渲染器的行状态模型与终端现实失步且不自愈（探针矩阵实锤：纯中文 / 纯英文不乱、中英混合才乱）。对策：文本键后强制整屏重绘（`tea.ClearScreen`）；真实解码器测试锁定输入链路，SMOKE T10 回归通过。
- **T9 插话回显吞行 / 覆盖**（2026-11-01 → 11-04，六次复现收口）：字节级根因——帧高变化的 flush 触发 ED 全量擦屏，且从 insertAbove 遗留在历史区的真实光标处向下擦。三层修复：落盘批次收敛单队列全局保序（`879d41b`）+ 帧高冻结（`8f9cac0`）+ 插话 / 工具行随流式块落盘（`b9b1cb1`）；插话投递缺失另修（`c3418eb`）。✅ 已核销（2026-11-04，会话 20261104-003832-1800）。
- **长回答定稿错位**（`6e04ae0`，2026-09-05）：见 Added「滚动缓冲区渐进落盘」——insertAbove 视口不变式击穿的根治。
- **工具参数截断致死会话**（`129a1f2`，2026-11-03）：上游截断的 JSON 参数使会话日志 Marshal 失败 → appendFatal 终止会话；改为 `json.Valid` 语法 + schema required 语义双重门控，非法参数按提供方错误走既有重试路径返给模型，日志只记合法占位（I1 可重放保持）。

### Fixed — 其他

- Windows bash 执行路径：避开 System32 / WindowsApps 的 WSL launcher，失败时 stderr 并入输出使模型可诊断（`4569556`，2026-11-04）。
- `↑` 输入历史召回失效（`83c3f00`，2026-11-04）：`loadHistory` 值接收者改指针；支持连续上翻、`↓` 回到实时输入。
- 显示层空行污染（`4420f37`，2026-11-04）：纯空白 assistant 正文与首尾换行 padding 过滤；日志与模型投影保留原文（I1「写什么发什么」不动）。
- slash 命令先于 busy 分流（`84b8a87`，2026-11-12）：生成中 `/jobs` 等命令不再被当插话发给模型；退出击杀 job 前等结算落盘（`ebd1f29`，`/resume` 转录补齐 killed 结算）。
- 早期修复（2026-08-23 → 09-05）：日志写失败快速失败（`858f1b2`）、`.env` 相对路径密钥丢失（`0b3d43b`）、启动提示 `⚠` 改 ASCII `[!]`（`c741117`）、grep 大目录 Esc 即时生效（`5e93d06`）、状态栏极窄终端生成中标记误丢（`41c0861`）、思考行只闪最后 token（`7006f8a`）、滚动落盘制表位展开（`9d90feb`）、选择器超长列表滚动跟随（`9cdd0a3`）、`/skill` 全局目录重复拼接与本地模型 HTTP 500（`e5062f5`）。

### 已知未覆盖（发版时点）

- 三终端实机冒烟：Windows Terminal 已过（T1–T14 全部登记通过，[docs/SMOKE.md](docs/SMOKE.md) §4）；macOS iTerm2 / Linux 终端未测（DEBT 常设项）。
- Windows 主机 PATH 首个 bash 为 niubash（MSYS）时，`TestBashTimeout` / `TestBashBackgroundProcess` 两例环境性失败（niubash 对后台孤儿的驻留语义；生产 `executeWithJobs` 路径不受影响，DEBT 登记，语义由 Linux 锁定）。
- T15 steer 真打断未立项（条件触发，[docs/SMOKE.md](docs/SMOKE.md) §5）。
