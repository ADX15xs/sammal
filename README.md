# Sammal — 极简个人 Agent Harness

**Sammal**（芬兰语：苔藓）是一个本地优先的极简 AI 编程助手 harness：Go 语言编写，编译为跨平台单二进制（Windows / macOS / Linux × amd64 / arm64），通过 OpenAI 兼容协议对接 Ollama、LM Studio、vLLM 及各类云端端点。

> 本项目以 [docs/SPEC.md](docs/SPEC.md) 为开发主文档——设计依据、六条不变量、模块规格与里程碑全部在那里，本 README 只做导览。

## 苔藓哲学

苔藓生长缓慢却坚韧，柔软却能紧紧附着在岩石之上。Sammal 的附着力来自三条实践：

- **不造轮子，只组轮子**——核心只写 agent 循环、会话与胶合逻辑；渲染交给 Bubble Tea v2，推理交给模型，编辑交给 `$EDITOR`，滚动/搜索/复制交给终端原生能力
- **按需生长，不变量守底**——功能数量不设上限，但六条可测试的不变量永不击穿（见下）
- **零隐性债**——一切妥协显式记入 `DEBT.md`，写明为什么妥协、怎么还、何时还

## 六条不变量

任何功能生长都不得击穿以下边界（完整可测试定义见规格书第 2 章）：

| # | 不变量 | 一句话 |
|---|--------|--------|
| I1 | 模型可见 = 已写入日志 | 发给模型的一切都能从会话日志重建 |
| I2 | 请求前缀字节级稳定 | 系统提示词与工具 schema 静态；动态状态只注入 user turn 尾部 |
| I3 | 会话可从日志完整重建 | 恢复、分支、压缩、回放共享同一份投影 |
| I4 | 单渲染路径 | 只有内联滚动模式，永不引入 alt-screen 双路径 |
| I5 | 工具输出与渲染投影分离 | canonical value 给模型和日志，投影给界面 |
| I6 | 零隐性债 | 妥协必须显式记账 |

## 特性一览

- **内联滚动 TUI**：定稿输出追加进终端原生滚动缓冲区，自绘面只有底部输入区——保留原生滚动、搜索、复制，CJK 输入从架构上安全
- **agent 工具环六件套**：`read` / `write` / `edit` / `bash` / `grep` / `glob`
- **YOLO 全自动 + 快照回滚**：工具直接执行不打断；写操作前自动做 per-turn 文件快照，`/rewind` 一键回滚代码与对话
- **事件溯源会话**：JSONL 日志是唯一真相；支持恢复、分支、崩溃恢复
- **上下文压缩**：0.8× 窗口触发 → 工具输出剪枝 → 结构化摘要 → 保留 0.16× 原文尾部；摘要请求逐字重放前缀以复用 KV 缓存
- **缓存纪律**：请求前缀字节级稳定，同模型多轮下前缀缓存持续命中（切换模型必然重新预填充，属预期行为）
- **快捷交互**：`Ctrl+P` 模型切换，`Ctrl+E` 调用 `$EDITOR` 编辑长提示，`Esc` 中止生成

## 架构

```
┌─────────────────────────────────────────────┐
│ sammal（终端，内联滚动模式）                    │
│ 定稿输出 → 追加进原生滚动缓冲区                  │
│ 自绘面 = 底部输入区 + Ctrl+P 选择器             │
└──────────────────┬──────────────────────────┘
                   │ 订阅事件流（唯一通道）
┌──────────────────▼──────────────────────────┐
│ core（无 UI 核心）                             │
│ agent loop（turn/step 状态机）                 │
│ 六件套工具 │ per-turn 快照 │ 上下文压缩          │
└─────────┬──────────────────────┬────────────┘
          │                      │
┌─────────▼─────────┐  ┌────────▼──────────────┐
│ provider           │  │ session                │
│ OpenAI 兼容协议     │  │ 事件溯源 JSONL 日志     │
│ SSE 流式 + 工具调用 │  │ 恢复 / 分支 / 崩溃恢复  │
│ 停滞看门狗 + 限流退避重连   │  │ （唯一真相）            │
└───────────────────┘  └───────────────────────┘
```

core 与 tui 分离不是为了未来扩展，而是三个当下必然：事件日志是会话功能的唯一真相（tui 只能是它的投影）、流式推理与输入渲染天然并发、核心可脱离终端做测试。

## 与参照系对比

| 维度 | Reasonix | DeepSeek Harness (dsh) | Sammal |
|------|----------|------------------------|--------|
| 界面 | Bubble Tea v2 全屏接管 | 本地 Web + 浏览器（无 TUI） | Bubble Tea v2 内联滚动 |
| CJK 策略 | 三处平台补丁 + 终端特判 | 浏览器原生解决 | 追加式渲染天然宽容 + 显式策略 |
| 规模 | 72.8 万行 / 8387 commit | 24 万行 / 13147 commit（2.5 个月） | 按需生长，六不变量守底 |
| 会话真相 | JSONL + 多 sidecar | 事件溯源 JSONL（zstd） | 事件溯源 JSONL |
| 压缩配方 | 0.8 触发 / 0.16 尾部 | 0.8 触发 / 0.16 尾部 | 同配方（三方收敛点） |
| 生长方式 | repolint 棘轮管理存量债 | 微内核插件化（227 包） | 不变量测试 + DEBT.md 显式记账 |

## 快速开始

### 安装

从 [Releases](../../releases) 下载对应平台产物（Windows / macOS / Linux × amd64 / arm64，纯 Go 静态单二进制），或自行构建：

```bash
git clone <repo> && cd sammal
go build -o sammal ./cmd/sammal      # 或 goreleaser release --snapshot --clean（低内存机器加 --parallelism 1）
```

### 配置

创建 `~/.config/sammal/config.toml`（Windows：`%APPDATA%\sammal\config.toml`）：

```toml
default_model = "qwen3-local"        # 日常启动即用

[models.qwen3-local]                 # 本地 Ollama 主力
base_url       = "http://localhost:11434/v1"
model          = "qwen3:32b"
context_window = 131072              # 压缩触发阈值依赖它

[models.deepseek]                    # 云端备选，Ctrl+P 切换
base_url       = "https://api.deepseek.com"
model          = "deepseek-v4-flash"
api_key_env    = "DEEPSEEK_API_KEY"  # 可选；本地端点可不设
context_window = 131072

[ui]
editor = ""                          # Ctrl+E 默认 $VISUAL/$EDITOR，可强制指定
```

密钥不必设成 Windows 全局环境变量——在配置同目录放一个 `.env` 即可：

```
# %APPDATA%\sammal\.env（Linux/macOS: ~/.config/sammal/.env）
DEEPSEEK_API_KEY=sk-xxxx
```

同名进程环境变量始终优先于 `.env`（CI/会话注入不被覆盖）；
配置了 `api_key_env` 而两处都没有值时，启动时会提示写入位置。

在配置文件所在目录的任意子目录运行 `sammal`，会话与快照存储在
`~/.local/share/sammal/sessions/`（Windows：`%LOCALAPPDATA%\sammal\sessions\`）。

### 断流重连

网络错误、流停滞、429 限流、5xx 服务端错误都会在 step 边界自动重连，重试同一请求（逐字节一致，KV 缓存友好）。重连策略内置在代码里，不可配置；旧配置中的 `retry_max` / `rate_limit_budget` 键会被忽略。

- 断流重连上限为 **5 次**，超出后上抛错误终止 turn
- 退避曲线（单次等待上限 `60s`）：
  - **普通断流**（网络/停滞/5xx）：`1s`、`2s`、`4s`、`8s`、`16s`、`32s`、…
  - **429 限流**：同一轮请求内连续命中超过 1 次后，基础退避从 1s 切换到 5s（`1s` → `10s` → `20s` → `40s` → `60s`），总耐心约 2 分钟，覆盖大多数分钟级限流窗口
- 端点提供 `Retry-After` 时取 `max(退避, Retry-After)`；要求等待超过 `60s` 即视为订阅 plan 用量窗口，立即快速失败并注明恢复时间点
- 429 命中配额特征串（`usage limit` 等）直接快速失败，不消耗重试预算

### 键位

| 键 | 行为 |
|---|---|
| `Enter` | 发送输入 |
| `Esc` | 中止当前生成（部分内容标记中断后保留） |
| `Ctrl+C` | 生成中 = 中止；空闲空输入 = 退出 |
| `Ctrl+P` | 模型选择器（模糊过滤，Enter 切换） |
| `Ctrl+E` | `$EDITOR` 编辑长输入（多行/粘贴大段的主路径） |
| `↑` / `↓`（输入空时） | 翻阅历史输入 |

### 命令

```
/model [name]  切换模型；无参列出（历史完整保留，KV 缓存如实重建）
/new           开新会话
/resume [n]    恢复历史会话；无参列出
/branch        从当前 turn 分叉探索
/compact       手动触发上下文压缩
/rewind [n]    回滚代码与对话到 turn n 之前（仅文件写操作，不含 bash 副作用）
/help          命令自述
```

### 终端兼容矩阵

承诺：Windows Terminal、iTerm2、主流 Linux 终端（GNOME Terminal / Konsole / Alacritty / kitty）。
明确不承诺：conhost、mintty、Warp、Termux——渲染异常记 [DEBT.md](DEBT.md) 不修。

## 名字的由来

**Sammal** 在芬兰语中意为"苔藓"。苔藓生长缓慢却坚韧，柔软却能紧紧附着在岩石之上。Sammal 不喧宾夺主——推理归模型，渲染归成熟库，滚动搜索复制归终端自己——它只做那层让一切协同的薄薄的苔藓。
