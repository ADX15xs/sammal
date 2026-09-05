# Sammal 开发技术规格计划书

> 本文档是 Sammal 的开发主文档。设计决策、不变量、模块规格与里程碑以此为准；README 只做导览。
>
> 状态：实现阶段——M0–M4 已全部交付（2026-08-23，git log 与第 9 章为证），此后特性增量以文末「决策变更记录」追加为准。决策记录格式：本章陈述为基线，后续变更在文末「决策变更记录」追加，不静默修改。

---

## 第 1 章 项目定位与强化后的目标

### 1.1 第一性推导：从痛点到本质需求

起点是一份个人构想（tmux + fzf + <200 行 Python 调度器的"胶水助手"），经过对 Pi、Reasonix、DeepSeek Harness 三个成熟参照系的调研与逐层拷问，需求收敛如下：

| 动机 | 本质需求 |
|---|---|
| 中文输入错位——自绘输入区 × 终端增量渲染 × CJK 双宽字素的系统性冲突（Reasonix 使用体验） | 输入交给终端原生能力，自绘面越小越好 |
| 菜单臃肿、BUG 频发——fix:feat ≈ 2.2:1（Reasonix 使用体验，代码级证据见 4.3） | 复杂度只花在核心循环上，界面层极薄 |
| 缓存命中率要可保证且可观测（勘误见下方注） | 请求前缀字节级稳定作为硬纪律，命中率作为指标透出 |
| 维护成本持续投入 | 依赖成熟轮子（Bubble Tea v2 等），核心小而可测 |
| 无法在 Windows 原生使用（tmux 断点） | Go 跨平台单二进制 |
| 需要真正的编程能力而非聊天 | v1 即具备 agent 工具环 |

> **勘误（2026-08-23）**：原始构想（README 初稿）曾把"缓存命中率不可控"记为 Reasonix 的对比劣势。代码级调研证伪：Reasonix 自早期设计起即以不可变系统提示词前缀为铁律，并有 CI 门禁（Cache-impact 元数据）与工具契约锁测试保障（见 4.1/4.2）。Sammal 的缓存纪律定位是**继承**该铁律并做减法（个人项目以收尾自查替代 CI 机器），新增的增量要求只有一条：命中率可观测（usage 透出，见 6.1）。

### 1.2 强化后的目标表述

**规模不设上限，质量下限设死。**

Sammal 是一个随需求生长、始终满足六条不变量（第 2 章）、零隐性债的个人 agent harness。它不以"媲美生产产品"为目标——Reasonix 的 72.8 万行和 dsh 的 24 万行中，绝大部分是为产品化广度（终端兼容矩阵、多前端、插件生态、企业功能）支付的，个人 harness 不支付这部分。

成功标准（可验证）：

1. 日常编码任务在本地模型（Qwen 级）上可用工具环完成
2. CJK 输入零 workaround（无平台补丁、无终端特判散布）
3. 缓存命中率可观测（usage 中 `prompt_cache_hit_tokens` / `cached_tokens` 透出为指标）
4. 会话日志可重放（从日志重建出与当时完全一致的模型请求）
5. 单人可维护（理解全核心无需一周）

### 1.3 项目身份

- 名称：Sammal（芬兰语：苔藓），哲学叙事见 README
- 语言：Go（纯 Go，`CGO_ENABLED=0` 静态单二进制）
- 平台矩阵：Windows / macOS / Linux × amd64 / arm64（goreleaser 六目标）
- 协议：v1 仅 OpenAI 兼容协议（`/v1/chat/completions`），覆盖 Ollama、LM Studio、vLLM、DeepSeek、OpenRouter 等
- 用户：开发者本人（单人使用场景塑造一切取舍）

---

## 第 2 章 六条不变量

这是本项目的稳固机制。此前考虑过"硬性 Non-goals 栅栏"并被否决——按需生长不限制功能数量；取而代之，**质量下限以不变量的形式设死**：任何功能生长不得击穿以下边界，每条附可测试定义。

### I1 模型可见 = 已写入日志

发给模型的任何内容（消息、工具结果、系统提示词形态）必须能从会话日志重建。

- **测试**：golden 请求测试——重放会话日志，逐步重建模型请求，与日志中记录的 `request/header` 逐字节比对
- **来源**：dsh 的核心不变量（"Model-visible means logged"），三家参照系共同收敛

### I2 请求前缀字节级稳定

系统提示词 + 工具 schema 在会话内跨轮字节级不变；一切动态状态（语言偏好、当前计划、记忆增量、当天日期）只进 user turn（系统提示词之后的可变区），不进系统提示词。当天日期由 agent 在每条 user 消息提交时以独立 `date` 字段落盘（UserMessageData.Date），模型投影（DeriveMessages）时展开为消息头部的 `Today: YYYY-MM-DD` 前缀——日期随消息成为该 turn 固定的历史事实：系统提示词全程字节稳定、前缀缓存全程命中，跨零点只影响新 turn，重放可复现；消息正文不含注记，转录回放等人类消费面与模型投影各取所需（投影纯函数）。

- **测试**：系统提示词与工具 schema 序列化有 golden bytes 测试；同一会话相邻两轮请求的公共前缀长度 = 上一轮请求全长
- **推论**：模式切换类功能不得增删工具目录；模型切换必然重建 KV 缓存（按模型隔离），属预期行为，文档如实陈述，不宣称"100% 命中率"

#### I2 附：前缀边界定义

"请求前缀"由以下部分构成，会话内字节级不变：

- **系统提示词**：`agent.BuildSystemPrompt` 的输入来自 `SessionHeader`（cwd/os/shell/date/AGENTS.md 内容），会话首拍定格，resume 时从 header 重建，字节一致
- **工具目录**：`tool.Registry.Defs()` 返回各工具的 `Name`/`Description`/`Schema` 常量。`BashTool.Schema()` 的 shellNote 插值是唯一允许的动态项，定格于会话创建
- **消息历史**：`session.DeriveMessages()` 通过 `projector` 确定性投影日志。turn 间的工具结果投影受剪枝策略影响（见下文豁免区）

**注 —— 剪枝豁免区**：`session.go` 中 `projector.messages()` 对超过 `pruneThreshold`（8KB）的旧 turn 工具结果做头尾截断，**是唯一允许的前缀非单调变化**。新增剪枝路径须过 I1 重放测试。

**注 —— 图片放置规则**：多模态图片不进消息投影，只在请求构造时经 `session.AttachImageParts` 追加到最后一条 user 消息尾部——带图请求的前缀因此与无图会话逐字节一致。放置与重放还原机制见 6.9。

**新增请求类型模式**：`agent/commands.go:compact()` 展示了如何构造一个复用前缀缓存的请求（逐字重放 + 尾部追加指令）。新增类似请求应遵循此模式。

### I3 会话可从日志完整重建

resume、branch、compaction、TUI 重绘、回放全部是同一份事件日志的投影，不维护影子状态。

- **测试**：随机时刻 kill 进程 → 重新打开会话 → 状态一致（有效尾部 + 合成闭合事件）；TUI 重绘与重放投影结果一致

### I4 单渲染路径

只有内联滚动模式一种渲染路径，永不引入 alt-screen 双路径（Reasonix 的 `if m.nativeScrollback` 分支散布 20+ 处是反面教材）。

- **测试**：代码审查清单项——禁止出现 `WithAltScreen(true)`；不按终端型号分叉渲染逻辑

### I5 工具输出 = canonical value 与渲染投影分离

工具 `Execute` 返回无损的结构化结果（写入日志、供模型消费）；「模型看到什么（截断策略）」「界面画什么」是独立的投影函数。

- **测试**：工具单测只针对 canonical value；投影函数纯函数化、单独测试；压缩剪枝只改投影不改日志

### I6 零隐性债

一切妥协（临时方案、未做的测试、已知怪癖）显式记入 `DEBT.md`：为什么妥协、怎么还、何时还。

- **测试**：每里程碑收尾核对账本与代码内 `TODO/FIXME` 一致；新增 workaround 而未记账视为验收失败

---

## 第 3 章 纯净开发纪律

开发过程的准则（写每一行代码时遵守），不是收尾时的清理动作。收尾只做验证；若收尾需要大量清理，说明过程没守住。

### 3.1 写时即对

- **命名清晰**：名字直接表意；名字说不清意图时先改名，而不是加注释
- **注释少而精**：只写非显然的"为什么"（取舍、约束、边界原因），不写"做了什么"；代码可自解释时不写注释
- **不留死代码**：不用的导入/变量/函数随手删，不注释掉留"备胎"
- **不制造重复**：写第二遍之前先想复用；但提取的代价大于重复时宁可不提（可读性优先）

### 3.2 YAGNI 验收标准

每一行代码、每一个配置键、每一个接口必须有**当下成立**的理由：

- 接口仅在存在 ≥2 个当前实现或测试替身时引入（例：`Provider` 接口的理由是 httptest 假服务器，不是"未来支持 Anthropic"）
- 不为想象中的需求做抽象、泛化、配置化、预留扩展点
- 配置键必须能说出当下的用户故事（第 7.2 节逐键登记）

### 3.3 显式负债机制

- `DEBT.md` 置于仓库根目录，条目格式：`| 位置 | 为什么妥协 | 怎么还 | 何时还 |`
- 代码内 `TODO`/`FIXME` 必须带原因与关联账本条目，禁止裸 TODO
- 典型入账场景：某 OpenAI 兼容端点的怪癖绕行、暂缓的测试、简化的实现

### 3.4 里程碑收尾验证清单

每个里程碑（第 9 章）结束前执行：

1. `git diff` 逐项过本次改动：命名表意？注释少而精？无死代码/调试残留？无顺手过度设计？
2. 不变量测试全绿（I1–I5 的自动化测试）
3. `DEBT.md` 核对：新增妥协全部入账，到期条目处理或改期
4. 缓存影响自查（继承 dsh 的审查文化，不做 CI 机器）：本次改动是否触及请求前缀？触及则回答"如何保持字节级稳定"

---

## 第 4 章 调研依据

三个参照系的实地调研结论（Reasonix 与 dsh 为本地仓库代码级调研，Pi 为公开资料）。

### 4.1 三方证据

| 维度 | Pi | Reasonix | DeepSeek Harness (dsh) |
|---|---|---|---|
| 语言/栈 | TypeScript + Bun 单二进制 | 纯 Go + Bubble Tea v2 | TypeScript + Cordis 微内核（227 包） |
| 界面 | 自研 pi-tui，追加式滚动 | Bubble Tea v2 全屏接管 + Termux 降级双路径 | 无 TUI，本地 Web + 浏览器 |
| 工具面 | 4 个（read/write/edit/bash） | ~20 个 | ~50 个（25 个包） |
| agent loop | ~418 行 | internal/agent 93.7K 行 | ~1600 行（core/agent-loop） |
| 会话真相 | JSONL | JSONL + 多 sidecar | 事件溯源 JSONL（zstd 帧） |
| 压缩 | — | 0.8× 触发 / 0.16× 尾部 / 结构化摘要 | 0.8× 触发 / 0.16× 尾部 / 结构化摘要（几乎逐字相同） |
| 缓存纪律 | 静态前缀 | 静态前缀 + CI 门禁（Cache-impact 元数据） | 每包 README 强制 "KV Cache effect" 小节 |
| 规模 | 单人可维护 | 72.8 万行 / 8387 commit / fix:feat 2.2:1 | 24 万行 / 13147 commit / 2.5 个月冲刺 |

### 4.2 继承清单（低成本高回报，已写入模块规格）

| 做法 | 来源 | 落点 |
|---|---|---|
| 事件溯源 JSONL 作为唯一真相 | 三方收敛 | 第 6.5 节 session |
| 0.8× 触发 / 0.16× 原文尾部 / 结构化摘要模板 / 摘要请求逐字重放前缀 | Reasonix + dsh | 第 6.6 节 compaction |
| turn/step 循环词汇 + 消息收件箱（用户 steering 在步骤边界吸收） | dsh | 第 6.2 节 agent loop |
| 中止时给未执行的工具调用补合成错误结果（保证可重放） | dsh | 第 6.2 节 |
| bash→PowerShell 自动降级 + 向模型注入"写 PowerShell"提示 | Reasonix | 第 6.3 节 tools |
| SSE 停滞看门狗 + 断流分类重连 | Reasonix | 第 6.1 节 provider |
| git-free per-turn 文件快照 + /rewind 同时回滚代码与对话 | Reasonix checkpoint | 第 6.4 节 |
| 动态状态骑 user turn 尾部 | Reasonix + dsh | 不变量 I2 |
| 会话在 turn 边界 fork 实现分支 | dsh sessions.fork | 第 6.5 节 |
| 崩溃恢复保留有效尾部 + 合成闭合事件 | dsh | 第 6.5 节 |

### 4.3 避开清单（有代码级证据的坑）

| 坑 | 证据 | 对策 |
|---|---|---|
| 双渲染路径（alt-screen + native scrollback） | Reasonix 每个渲染函数双分支，20+ 处 | 不变量 I4 |
| overlay 手工互斥（字段判 nil 链） | Reasonix `hideComposer()` 注释亲口要求加菜单时人肉同步两处 | 弹窗状态用一个枚举集中管理（第 6.7 节） |
| 单 struct 巨型 TUI | Reasonix chatTUI 5484 行 / 163 个顶层成员 | 按面板拆 component，单文件 ≤800 行棘轮 |
| CJK × 增量渲染 | Reasonix 三处平台补丁（Windows 强制清屏、mintty NBSP、bar 光标） | 内联模式自绘面最小化 + 显式 CJK 策略（第 6.7 节） |
| 终端特判散布（Warp/mintty/conhost/Termux 各一套） | Reasonix 各处 workaround | 终端能力探测集中一处；兼容矩阵收窄（第 10 章） |
| 微内核插件化组合爆炸 | dsh 227 个 package.json、四类事件分发语义 | Go interface 足够，不引入插件框架 |
| 为未来预留 transport seam | （本计划早先版本自查出的过度设计） | 架构动机只写当下必然（第 5 章） |
| slash 命令平铺膨胀 | Reasonix 45+ 命令 | 命令集最小化，逐个登记理由（第 8.2 节） |

---

## 第 5 章 总体架构

### 5.1 分层

```
cmd/sammal          薄入口：解析参数、装配、启动
internal/tui        Bubble Tea v2 内联滚动模式（唯一前端，事件流的订阅者）
internal/skill      /skill 的扫描、解析与展开（prompt 简化器，TUI 叶子包）
internal/agent      turn/step 状态机、消息收件箱、中止语义（无 UI）
internal/tool       Tool 接口 + 六件套 + canonical/render 投影
internal/provider   OpenAI 兼容 SSE 客户端（流式 + 工具调用聚合 + 看门狗）
internal/session    事件溯源 JSONL 日志（唯一真相）、恢复、分支、崩溃恢复
internal/compaction 上下文压缩（剪枝 + 摘要 + 前缀重放）
internal/checkpoint per-turn 文件快照与回滚
internal/config     config.toml 加载
internal/human      面向终端用户的紧凑数值渲染（时长等），tui/agent/provider/tool 共用——保证同一用户看到的格式一致
```

依赖方向单向向下：tui → agent → {tool, provider, session, compaction, checkpoint} → config，tui → skill（叶子），human 为无内部依赖的叶子（tui/agent/provider/tool 共用）。session 不依赖任何上层。

### 5.2 core 与 tui 分离的三个当下依据

（不为未来扩展预留，每个理由现在就成立）

1. **事件日志是会话功能的唯一真相**：resume/branch/compaction/回放（已锁定的需求）都要求从日志投影，TUI 只能是投影的消费者之一
2. **流式推理与输入渲染天然并发**：agent 循环在 goroutine 中跑流式 + 工具执行，TUI 事件循环独立响应按键；不分离会复刻 Reasonix 的 god-object
3. **核心可脱离终端测试**：agent/tool/session 的测试不需要 TTY

### 5.3 事件流

core 对外的唯一接口是事件流（Go channel，事件类型见 6.5.2）。TUI 订阅事件渲染；`request/header` 类事件同时落日志。将来若真需要 Web 面板或 ACP，也只是新增一个订阅者——这是生长的可能性，不是现在的设计依据。

---

## 第 6 章 模块规格

### 6.1 provider — OpenAI 兼容客户端

**职责**：把 OpenAI 兼容的 SSE 流式响应转成规范化的 Chunk 流，聚合 tool_calls 增量。

```go
type Provider interface {
    // 引入理由：httptest 假服务器是第二个当前实现（测试替身），不是为未来协议预留
    Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

type Chunk struct {
    TextDelta    string          // 文本增量
    ReasonDelta  string          // reasoning_content / reasoning 双字段兼容；落日志不投影（8.3）
    ToolCallDelta ToolCallDelta  // tool_calls 增量（按 index 聚合）
    Usage        *Usage          // finish 前保证送达；透出缓存命中字段
    FinishReason string
}
```

关键设计：

- 端点：`POST {base_url}/chat/completions`，`stream: true`，`stream_options.include_usage` 恒开
- **SSE 停滞看门狗**：默认 300s 无 chunk 判定停滞，抛 `StreamInterruptedError`（区分网络错误 / 流停滞 / 协议错误 / 上下文溢出 / 限流 / 服务端错误 / 配额窗口），仅在 step 边界重连
- **非 200 分类**（`classifyHTTPError`）：429 且响应体命中配额特征串（usage limit / limit reached 等）→ **配额窗口**；其余 429 → **限流**；5xx → **服务端错误**；400+溢出特征 → **上下文溢出**；其余 → 协议错误。限流/服务端错误附带 `RetryAfter`：解析 `Retry-After`（整数秒 / HTTP-date），best-effort 兼容 `x-ratelimit-reset`（unix 秒 / RFC3339）；过去时点与无法识别的格式一律视为未告知
- `Usage` 透出 `prompt_cache_hit_tokens`（DeepSeek 系）与 `cached_tokens`（OpenAI 系），供 I2 的可观测指标
- 请求体序列化确定性（字段顺序稳定），支撑 I2 的前缀比对测试
- **不传思考控制参数**：`reasoning_effort`、thinking 开关类参数在 OpenAI 兼容生态无标准（OpenAI 系 `reasoning_effort`、DeepSeek `thinking.type`、Qwen3 @ vLLM `chat_template_kwargs.enable_thinking`、Ollama `think`），逐个透传即端点特判起步（7.2 收敛原则）；思考强度用端点默认，想换档位注册两个模型条目走 Ctrl+P。翻案条件：同一部署形态下的思考翻转成为日常用法（如 vLLM/LM Studio 统一部署的混合思考模型，此时双条目无出路）再按端点逐个实现

### 6.2 agent — 循环与事件

**职责**：turn/step 状态机；驱动模型 ↔ 工具直到模型停止调用工具。

```go
type Agent struct { /* session 日志、工具注册表、provider、收件箱 */ }

func (a *Agent) Run(ctx context.Context, userMsg string) error  // 一个 turn
```

循环结构（一个 turn 内若干 step）：

```
turn/start
loop:
  pre-step：吸收收件箱中的用户 steering 消息（追加为 user 消息）
  step/start
  → provider.Stream（assistant/chunk 事件流式透出）
  → assistant/message 定稿（含被中止时的部分内容 + interrupted 标记）
  → 若有 tool_calls：顺序执行（v1 不并行），每个 tool/call、tool/result 落日志
  → 模型不再调用工具 → turn/end
```

关键设计：

- **消息收件箱**：生成期间用户输入进入队列（不丢弃、不注入进行中的请求），在下一个 step 边界吸收——用户可以随时插话纠偏
- **断流分类重连**：网络/停滞/限流/服务端错误在 step 边界重连，重试同一请求（逐字节一致，I2/KV 缓存友好）。退避 = max(基础退避指数增长, Retry-After)，单次等待上限 60s；要求等待超过 60s 即订阅 plan 的用量窗口（小时级），立即放弃重试并注明恢复时间点；429 命中配额特征串同样直接上抛。重试上限与退避曲线**内置不可配置**：上限 5 次；同一 step 内连续 429 超过 1 次后基础退避从 1s 切到 5s（两段式），对分钟级限流窗口的总耐心约 131s
- **中止语义**：`Esc` 触发 AbortSignal 贯穿流与工具执行；已产出部分内容标记 `interrupted` 存为 assistant 消息；**未执行的工具调用写入合成错误结果**（"aborted before execution"），保证 I1/I3 可重放
- **无 max-steps**：循环跑到模型自己停（token 压力由 compaction 兜底）；若实测需要上限，作为 DEBT 记账后加
- v1 工具顺序执行；并行执行（有界池 + 按序提交结果，dsh 方案）是明确的生长点，不在 v1 设计

### 6.3 tool — 六件套与投影分离

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage   // 手写 JSON Schema，静态（I2）
    ReadOnly() bool            // true = 不触发快照
    Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result 是 canonical value（I5）：无损、结构化、入日志
// 「模型看到什么」「界面画什么」由独立投影函数决定：
func ForModel(r Result, budget int) string   // 截断策略在此，不改日志
func ForTUI(r Result) string
```

| 工具 | 语义要点 |
|---|---|
| `read` | 读文件，行号前缀；offset/limit 参数；canonical 含 `content` + `truncated` |
| `write` | 整文件写入；父目录自动创建 |
| `edit` | `old_string`/`new_string` 精确匹配且**必须唯一**；命中 0 或 >1 次都报错并回显命中数与上下文（不做 fuzzy 容错——YAGNI，模型自己会修正） |
| `bash` | 命令 + 超时（默认 120s）；工作目录 = 会话根；**平台降级**：PATH 中无 bash 的 Windows 自动切 PowerShell，并向模型注入"写 PowerShell 语法，不要用 bash 语法、不要用 `&&`"提示（Reasonix 验证过的一行解决一类错误） |
| `grep` | 内容搜索；PATH 中有 ripgrep 则委托，否则纯 Go 实现（walk + regexp）；输出 `path:line:text`，上限截断 |
| `glob` | 模式匹配文件列表，上限截断 |

工具 schema 合计目标 < 1500 token（Pi 用 4 工具做到 <1000；六件套略宽，可观测后收窄描述）。

### 6.4 审批与快照 — YOLO + 可回滚

**审批模式：YOLO 全自动**。个人本地环境信任模型，工具直接执行不打断；安全性由快照兜底。

- **per-turn 快照**（git-free，不依赖用户仓库状态）：仅写类工具（`write`/`edit`）在首次落盘前捕获涉及文件的原内容；`bash` 副作用**明确不追踪**（文档如实声明）
- 快照存储：会话目录下 `checkpoints/<turn-n>/`，文件级 blob + 路径清单
- 工作目录不是 git 仓库时，首次写操作前提示用户"建议 git init 以获得完整兜底"，不阻塞执行
- `/rewind <turn-n>`：同时回滚（a）快照覆盖的文件（b）会话日志截断到该 turn 之前——代码与对话一起回到过去

### 6.5 session — 事件溯源日志（唯一真相）

**职责**：append-only JSONL 事件日志；I1/I3 的载体。

#### 6.5.1 目录布局

```
~/.local/share/sammal/sessions/<normalized-cwd>/<session-id>/
    session.jsonl          # 事件日志（唯一真相）
    checkpoints/           # per-turn 快照
    assets/                # 内容寻址图片资产（首次带图提交时创建，6.9）
```

（Windows：`%LOCALAPPDATA%\sammal\sessions\...`；`<normalized-cwd>` 把路径分隔符等 unsafe 字符替换为 `-`）

#### 6.5.2 事件 schema

首行是不可变 `SessionHeader`；其后每行一个事件：

```jsonc
{"seq":1,"ts":"...","type":"session/header","data":{"id":"...","cwd":"...","model":"qwen3-local","created":"..."}}

// 事件类型（envelope 统一：seq / ts / type / data）
session/header                          // 会话身份与提示词事实；model 存配置键（唯一身份）
user/message        {"text":"...","images":["<sha256><ext>"]}   // images = assets/ 资产引用；投影不含图
assistant/chunk     {"delta":"...","kind":"text|reasoning"}  // 流式增量（UI 保真）；reasoning 落日志不投影
assistant/message   {"text":"...","toolCalls":[...],"interrupted":false,"synthetic":false}  // synthetic = 崩溃恢复自 chunk 合成
tool/call           {"id":"...","name":"edit","args":{...}}
tool/result         {"id":"...","canonical":{...},"synthetic":false}     // I5：canonical 入日志
request/header      {"prefixHash":"...","messageCount":n,"model":"<端点 model>","images":[...]}  // I1：请求形态留痕；images 供重放还原图片
compaction/happened {"summary":"...","summaryRange":[a,b],"keptFrom":c}
turn/end            {"turn":n,"stopReason":"...","synthetic":false}
```

#### 6.5.3 恢复 / 分支 / 崩溃恢复

- **resume**：读日志 → 投影出模型历史与 UI 转录 → 继续追加。模型历史永远从日志 `deriveMessages()` 投影，不维护影子数组
- **branch**：在 turn 边界 fork——把日志前缀完整复制到新 `<session-id>`，此后各自追加（dsh `sessions.fork` 同构；拒绝在未闭合 turn 中间分叉）
- **崩溃恢复**：重新打开时校验尾部，丢弃不完整事件，为未闭合的 turn/step/tool 合成闭合事件（标记 `synthetic: true`），保证 I3

性能备忘：dsh 对连续 assistant/chunk 打包行省 ~60% 空间（zstd 帧）——v1 纯追加不压缩，实测成为痛点时作为生长点记账再做。

### 6.6 compaction — 上下文压缩

**触发**：投影后的模型历史估算 token ≥ 0.8 × `context_window`（模型预设中配置），在 **turn 开始的 step 边界**检查——turn 内不触发：工具环正在消费自己产出的上下文，中途换前缀会破坏 step 语义（turn 内撑爆上下文的场景见 DEBT 记账）。

**配方**（Reasonix 与 dsh 独立收敛，直接继承）：

1. **工具输出剪枝**（model-free，先跑）：日志中旧 turn 的 `tool/result` 投影超过 8192 字符的，截为头 4096 + 尾 1024（只改投影，不改日志——I5）
2. **LLM 摘要**：仍超限则把被遮蔽区间交给模型产出结构化简报，固定模板：`当前任务 / 关键决定 / 相关文件 / 已遇错误 / 待办 / 下一步`
3. **保留尾部**：最新 0.16 × 窗口的 turn 原文保留（永不小于 2 个 turn）
4. **缓存复用**：摘要请求**逐字重放**原系统提示词 + 工具 schema + 被遮蔽消息，只在尾部追加摘要指令——前缀 KV 缓存直接命中（I2）
5. 摘要以 `<compacted-summary>` 包裹替换旧区间，`compaction/happened` 事件落日志（`summary` 存完整摘要文本）

摘要请求**不留痕** `request/header`：它是日志的确定性函数（遮蔽区间投影 + 常量模板），重放可重建；留痕反而破坏 `ReplayRequestHashes` 的投影语义。

### 6.7 tui — 内联滚动前端

**职责**：订阅事件流渲染；收集用户输入。Bubble Tea v2（2026-02 稳定版），**内联滚动模式**（不进 alt-screen，I4）。

渲染策略：

- **内容经安全通道渐进追加进原生滚动缓冲区**（`tea.Println` 语义）：assistant 回答随流式渐进落盘（闭合行超触发线即落盘、定稿补余），工具结果投影、状态行、用户回显同通道——终端原生滚动/搜索/复制全保留。单次落盘的折行总行数不得超过视口安全预算（宽度归一化 + 分批 + 巨行硬拆，见 `internal/tui/scroll.go`）：bubbletea 内联渲染器的 insertAbove 在打印块超出视口时永久失步，此预算是 I4 在应用侧的细化不变量
- **唯一的可变区**：底部一块——当前流式块（尾部至多 8 行）+ 思考行 + 状态栏一行 + 底部输入框（原地重绘仅限这一块，宽字符变化时整块全量重绘——显式策略，不做平台补丁）
- **思考行**（reasoning 增量，dsh 方案）：只渲染最新一行暗色文字 `- 思考中 <计时> | <正文>`，超宽走尾部跟随；定稿即整行撤出视窗（行为细节见 8.3）
- **状态栏**：常驻一行，段构成与丢弃优先级见 8.3
- **弹窗状态用一个枚举集中管理**（none / modelPicker…），避开 Reasonix 的 nil 链互斥
- `Ctrl+P` 模型选择器：输入框上方的内嵌列表，自带模糊过滤（不依赖外部 fzf）
- `Ctrl+E` 长输入：临时文件 + `tea.ExecProcess` 调 `$VISUAL`/`$EDITOR`，保存退出后内容进入输入框待发送
- Markdown 渲染 v1 不做（纯文本 + 保留换行），列为生长点

CJK 安全策略（集中声明，替代散布的终端特判）：

- 光标列计算用 uniseg 字素簇 + runewidth 宽度（依赖 `rivo/uniseg`、`mattn/go-runewidth`）
- 光标默认 bar 形状（block 会遮盖 CJK 双宽字符）
- 兼容矩阵内不按终端型号分叉；矩阵外终端的渲染异常记 DEBT 不修（第 10 章）

### 6.8 config — 配置

TOML，加载顺序：默认值 → 用户配置。每个键的当下用户故事见 7.2。

### 6.9 多模态图片

M4 后增量（2026-08-28/29，见决策变更记录）：

- **入口**：TUI 命令 `/attach <path...>`（无参列出，`-clear` 清空）累积待发送图片，随下一条消息提交；支持 png / jpg / jpeg / gif / webp，单张上限 20MB，任一图片校验失败则整体报错、本次提交中止
- **存储**：字节按内容寻址落会话目录 `assets/<sha256><ext>`，日志只存引用（文件名）——JSONL 不膨胀，同字节幂等去重
- **请求放置**：图片只进请求尾部（最后一条 user 消息 content 追加 `image_url` data URI part），不进投影——带图请求前缀与无图会话逐字节一致（I2）；随工具环每个请求在尾部重发（端点无状态）
- **重放还原**：`request/header.images` 记录每次请求携带的资产引用，重放据此重建带图请求；资产被外部删除时该图跳过、对应请求重放哈希不一致，属既定降级语义（DEBT 记账）
- **生命周期**：`/branch` 随日志复制全部资产到新会话；`/rewind` 截断后清理不再被引用的孤儿资产；生成中带图插话被静默丢弃（收件箱只承载文本，DEBT 记账）

### 6.10 AGENTS.md 注入与 /skill — prompt 简化器

M4 后增量（2026-08-29，见决策变更记录）。定位：skill 与项目指令本质是 prompt 工程，**明确调用优于自动触发**——sammal 不做模型自决的技能加载，只做确定性的正文展开。

- **AGENTS.md 注入**：会话创建时读取 `<cwd>/AGENTS.md`（不存在则为空），内容存入 `SessionHeader.agentsMd` 首拍定格（resume/branch 从 header 重建，字节一致；`/new` 从磁盘重读让修改即时生效），由 `BuildSystemPrompt` 渲染为 Project instructions 段。常驻注入必须短——它是注意力预算，不是文档
- **skill 布局**：`<configDir>/skills/<name>/SKILL.md`（全局）与 `<cwd>/.agents/skills/<name>/SKILL.md`（项目）两级扫描；frontmatter 逐行只认 `name`/`description`（缺 name 用目录名）；同名 skill 项目级覆盖全局
- **展开机制**：`/skill <name> [任务]` 由 TUI 把「skill 正文（`<skill name="...">` 包裹）+ 任务描述」拼成一条 user 消息提交——骑 user turn 尾部（I2 明文豁免区），系统提示词与工具目录零改动，I1 自动满足（普通消息落日志）。skill 正文与历史一起参与压缩投影
- **发现**：`/skill` 无参打开内嵌模糊选择器（复用模型选择器组件，弹窗枚举新增 `skillPicker`），Enter **回填输入框**而非发送——skill 的主用法是「正文 + 任务」拼接；参数解析精确同名优先、其余子序列模糊匹配，歧义列候选不发送
- **明确不做的**：skill 目录不进系统提示词（无 I2 冻结负担，选择器现扫现显）；不做 `.agents/rules`（AGENTS.md 即 rules）；不做 hooks（模型验证闭环替代机器强制）；不做项目级 config（7.2 收敛原则）。模型主动加载（skill 工具 + 目录进前缀）的翻案条件：反复需要对模型说「先读 xx skill」时再加

---

## 第 7 章 数据与配置格式

### 7.1 会话数据

见 6.5。日志即数据，无其他持久化形态（无 SQLite、无缓存库——需要时记账再生长）。

### 7.2 config.toml

```toml
# ~/.config/sammal/config.toml（Windows: %APPDATA%\sammal\config.toml）

default_model = "qwen3-local"        # 用户故事：日常启动即用，不想每次选

[models.qwen3-local]                 # 用户故事：本地 Ollama 主力模型
base_url      = "http://localhost:11434/v1"
model         = "qwen3:32b"
api_key_env   = "OLLAMA_API_KEY"     # 可选；本地端点可不设
context_window = 131072              # compaction 触发阈值依赖它；缺省 32768

[models.deepseek]                    # 用户故事：云端备选，Ctrl+P 切换
base_url      = "https://api.deepseek.com"
model         = "deepseek-v4-flash"
api_key_env   = "DEEPSEEK_API_KEY"
context_window = 131072

[ui]
editor = ""                          # 用户故事：Ctrl+E 默认 $VISUAL/$EDITOR，可强制指定
```

配置面收敛原则：新增键必须登记用户故事；宁可代码常量，不可配置膨胀。断流重连的次数与退避曲线即按此内置为代码常量（见 6.2）。

### 7.3 模型预设

首发内置三条预设（Ollama / LM Studio / DeepSeek 云端）作为文档示例而非硬编码——配置即预设，代码不含厂商特判（个别端点怪癖若绕行，入 DEBT）。

---

## 第 8 章 交互规格

### 8.1 键位表

| 键 | 行为 | 理由 |
|---|---|---|
| `Enter` | 发送输入 | 通用惯例 |
| `Ctrl+E` | `$EDITOR` 编辑长输入 | 原构想保留；多行/粘贴大段的主路径 |
| `Ctrl+P` | 模型选择器（模糊过滤） | 原构想保留；去外部 fzf 依赖 |
| `Esc` | 中止当前生成（abort 语义见 6.2） | 生成期最高频操作 |
| `Ctrl+C` | 生成中 = 中止；空闲空输入 = 退出；非空输入 = 清空，3 秒内再按退出 | 惯例；防误退 |
| `↑` / `↓`（输入框空时） | 翻阅历史输入 | 惯例 |

### 8.2 最小 slash 命令集（逐个登记理由）

| 命令 | 理由 |
|---|---|
| `/model [name]` | 无参打开选择器；模型切换是核心工作流 |
| `/new` | 开新会话（频繁操作不值得退出重启） |
| `/attach [path...]` | 图片输入入口：附加 / 列出 / `-clear` 清空待发送图片（TUI 侧管理，随下一条消息提交） |
| `/skill [name] [任务]` | prompt 简化器：skill 正文 + 任务描述拼成一条消息提交；无参打开选择器（TUI 侧展开，6.10） |
| `/resume` | 恢复历史会话（I3 的用户入口） |
| `/branch` | 从当前 turn 分叉探索（会话分支的用户入口） |
| `/compact` | 手动触发压缩（自动触发之外的逃生门） |
| `/rewind [n]` | 回滚代码与对话（快照的用户入口）；无参列出可回滚的 turn |
| `/help` | 命令自述 |

新增命令须在此表登记理由；不设别名、不做分组嵌套（避开 45+ 命令平铺的可达性灾难）。

### 8.3 流式渲染行为

- 文本增量：闭合行数超过触发线（6 行）即把头部行渐进落盘进滚动缓冲区（经 scroll.go 安全通道：单次落盘折行总行数 ≤ 视口预算），可变区只留尾部窗口；定稿补打剩余行。事件契约：`MessageFinal.Text` 与 TextDelta 累积逐字节一致，故「已落盘前缀 + 定稿补余 == 全文」。流中断重连（StreamRestarted）即重新生成，已落盘部分不可撤回，追加一行暗色作废标记
- 思考增量：只更新"当前流式块"（可变区）
- 思考（reasoning）：只渲染**最新一行**暗色文字 + 计时器（缓解等待焦虑的最小口子，dsh 方案）；增量按 token 到达，正文超宽走尾部跟随（保留最新 token，无省略号）；思考块闭合即整行撤出视窗，不留摘要。全文经 `assistant/chunk kind=reasoning` 落日志（人类回看），**不进模型投影**——发给模型的历史不含思考
- 工具调用：`tool/call` 到达时输出一行摘要（工具名 + 参数摘要）；`tool/result` 到达时追加投影（截断至可读长度）
- 状态行：当前模型、token 用量、缓存命中指标（I2 的可观测出口）、上下文窗口占用 `ctx %`（≥70% 黄、≥80% 压缩触发线红）、生成中显示 turn 计时与本轮工具调用数；usage 随每个 step 定稿更新，ctx% 因此跟随工具环推进。空间不足时按丢弃优先级裁剪：工具数(5) → cache(3) → in/out 与计时器(同优先级 2，先丢更靠左的 in/out) → ctx(1)；模型名与生成中标记永不丢（负优先级段不可裁剪）。等待期（已提交、TurnStarted 未到）状态栏显示「生成中 <spinner>」，spinner 帧随心跳每秒推进，避免静止误判卡死。达到压缩触发线时 turn 结束后追加一次文字预警（一轮只报一次），且该预警须排在耗时落款之前
- 回合耗时标记：turn 正常完成时在回答最后追加一行暗色「（耗时 1m30s · HH:MM 完成）」——自 TurnStarted 起算、覆盖工具环全程，并附本地完成时刻；中止/出错回合不打（用户中止的时长无信息量，错误详情已含原因），TurnStarted 之前的失败路径无计时起点同样不打

---

## 第 9 章 里程碑 M0–M4

每个里程碑的验收 = **功能测试 + 不变量测试 + 收尾纯净清单**（3.4 节）三重门。

### M0 走路骨架 —— 已交付（02fb687，2026-08-23）

范围：provider（SSE 流式，无工具调用）+ tui 内联模式 + 单轮对话（无会话持久化）。

- 验收：流式输出追加进滚动缓冲区；`Enter` 发送；`Esc` 中止；CJK 输入/删除/光标移动正确（Windows Terminal + iTerm2 + Linux 终端各过一遍）
- 不变量测试起步：I2 前缀 golden bytes（系统提示词序列化确定性）

### M1 工具环 + 快照 —— 已交付（80e4526，2026-08-23）

范围：六件套工具 + agent 多 step 循环 + 收件箱 steering + 中止语义 + per-turn 快照 + `/rewind`。

- 验收：模型能用工具完成"读文件→改文件→跑命令验证"闭环；中止后日志可重放；`/rewind` 同时回滚文件与对话；无 bash 的 Windows 上 PowerShell 降级生效
- 不变量测试：I1 golden 请求重放、I5 投影分离单测

### M2 会话全套 —— 已交付（e8ba2f6，2026-08-23）

范围：JSONL 事件日志 + `/new` `/resume` `/branch` + compaction 全配方 + 崩溃恢复。

- 验收：kill -9 后 resume 状态一致；分支后两会话独立演进；0.8× 触发压缩且摘要请求前缀命中（usage 可证）；`/compact` 手动触发可用
- 不变量测试：I3 重放一致性、I2 跨轮前缀比对、压缩前缀重放命中

### M3 模型切换 + CJK 打磨 —— 已交付（c1e01e1，2026-08-23）

范围：`Ctrl+P` 选择器 + 多模型配置 + 切换语义（历史 carried、缓存重建如实提示）+ CJK 显式策略全量落地（uniseg 审计、bar 光标、宽字符整块重绘）。

- 验收：会话中途切模型历史不丢；切换后首条提示"KV 缓存已重建"（诚实呈现，不隐藏）；CJK 策略无终端型号分叉
- 不变量测试：全量回归

### M4 发布 —— 已交付（d470f76，2026-08-23）

范围：goreleaser 六目标交叉编译 + README/文档对齐 + DEBT.md 审计。

- 验收：六个平台产物可运行；文档与实现一致；账本无未核对的 TODO/FIXME
- 不变量测试：全量回归

里程碑之外的特性生长以「决策变更记录」为记录；修复与测试类提交见 git log。

---

## 第 10 章 风险与对策

| 风险 | 对策 |
|---|---|
| OpenAI 兼容端点怪癖（`reasoning_content` 字段变体、tool_calls 聚合差异、SSE 分包边界） | provider 双字段兼容起步；个别端点绕行入 DEBT 并登记端点名；测试用 httptest 假服务器覆盖已知情癖 |
| 终端兼容矩阵失控（Reasonix 每支持一个终端多一层分支的教训） | **收窄声明**：承诺 Windows Terminal、iTerm2、主流 Linux 终端（GNOME Terminal/Konsole/Alacritty/kitty）；conhost、mintty、Warp、Termux 明确不承诺，渲染异常记 DEBT 不修 |
| Bubble Tea v2 已知性能回归（部分场景 ~2x 渲染变慢） | 内联模式自绘面只有输入区 + 当前流式块，暴露面小；M0 起建立渲染延迟的体感基线，异常再评估 |
| 本地小模型工具调用不可靠 | 工具 schema 收窄（<1500 token）；系统提示词给出工具使用约束；模型预设文档标注推荐的工具调用能力档位 |
| 生长失控（本项目哲学允许按需生长） | 预警信号监控：单文件 >800 行棘轮（Reasonix 同规则）、slash 命令数、DEBT.md 增速、不变量测试通过率——任一报警即在里程碑收尾处理，不带病生长 |
| 快照不覆盖 bash 副作用（YOLO 的盲区） | 文档如实声明；`/rewind` 界面明示"仅回滚文件写操作"；建议 git init 提示常驻 |

---

## 第 11 章 参考来源

| 来源 | 用途 |
|---|---|
| [Pi Coding Agent](https://pi.dev/) / [作者博文](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/) | 内联追加式渲染、418 行循环、工具极简主义的可行性证明 |
| `D:\github-clone\DeepSeek-Reasonix`（本地调研） | Bubble Tea v2 生产实践、压缩配方、缓存纪律、CJK 坑的代码级证据 |
| `D:\github-clone\deepseek-harness`（本地调研） | 事件溯源不变量、turn/step 词汇、中止合成结果、fork 语义、KV cache 审查文化 |
| [Bubble Tea v2 发布公告](https://charm.land/blog/v2/) / [Releases](https://github.com/charmbracelet/bubbletea/releases) | 依赖稳定性依据与升级注意 |

---

## 决策变更记录

| 日期 | 决策 | 理由 |
|---|---|---|
| 2026-08-22 | 基线建立（本文档） | 三方调研 + 逐层拷问 + 纯净原则收敛 |
| 2026-08-23 | 勘误 1.1：撤销"Reasonix 缓存命中率不可控"的归因 | 代码调研证实其不可变前缀为早期设计铁律；Sammal 缓存纪律改为"继承 + 增量要求可观测性" |
| 2026-08-23 | I2 测试表述细化：「相邻请求公共前缀 = 上一请求全长」改为「system、工具目录与历史消息逐条序列化字节一致 + 请求体序列化确定性」 | JSON 嵌套结构（messages 数组闭括号）使整请求字节前缀按字面必然分叉；逐消息稳定才是服务端 token 前缀（KV 缓存）覆盖的内容，不变量本身不变 |
| 2026-08-23 | request/header 事件数据增加 model 字段（规格 6.5.2 最小示例之外） | 会话中途切模型（M3）后重放逐字节比对需要当时请求所用的模型名；I1 要求的必然推论 |
| 2026-08-23 | 配置面新增 secrets 能力（7.2 之外）：config.toml 同目录 `.env`（Windows `%APPDATA%\sammal\.env`）作为 `api_key_env` 的值兜底；同名进程环境变量优先 | 用户故事：Windows 调全局环境变量成本高；`.env` 跟随配置目录、不入日志、不占会话。优先序沿 dotenv 惯例（显式注入不被覆盖），本地端点不受影响 |
| 2026-08-24 | 断流分类扩展与限流应对：新增限流（429）/服务端错误（5xx）/配额窗口三档分类，429/5xx 纳入 step 边界重连（1s 指数退避、尊重 Retry-After、单次等待上限 60s）；配额特征或超限等待快速失败并注明恢复时间点；配置面每模型新增可选 `retry_max`（缺省 3） | 用户故事：线上 API 与免费端点 429 太常见，固定 1s×3 盲等熬不过分钟级配额窗口；coding/token plan 类订阅有 5 小时用量窗口，小时级等待循环重试无意义，应报恢复时间让用户自行重发。曲线常量不进配置（收敛原则），仅次数随端点差异可调 |
| 2026-08-25 | 日志写失败快速失败：全部落盘点统一经 appendFatal，首个写失败即上报并终止当前 turn；emit 在 root 取消后允许丢弃事件 | 日志已坏时继续执行只会产出不可重放的状态（I1）；磁盘满等持久性故障无法在 turn 内自愈，快速失败优于带病续跑。emit 防阻塞：消费端停止后 Submit/Slash 等同步调用方不能冻死在事件发送上 |
| 2026-08-26 | 思考链单行流式渲染与状态栏可观测：思考行 = 计时 + 最新一行，按 token 到达增量刷新，后移植 dsh 尾部跟随（超宽保留最新 token）；状态栏可观测增强（ctx 分级变色、负优先级段不可裁剪） | reasoning 落日志不投影的设计不变；渲染层只是把「等待中」做成最小的活性口子。省略号截断丢失最新 token，尾部跟随让推理流始终可见 |
| 2026-08-27 | session/header 的 model 字段改存配置键（用户可见名），不再存端点 model 字符串 | 同一端点 model 字符串可被多个配置复用（多个中转站转发同一模型），按端点字符串反查当前模型必然歧义；配置键才是唯一身份。I1 重放不受影响（request/header 仍存端点 model） |
| 2026-08-28 | 多模态图片输入：Message.Content 改多模态数组，TUI `/attach` 附加图片随消息提交；字节内容寻址落会话 assets/、日志只存引用；图片只进请求尾部不进投影，request/header 记录 images 引用（新增 6.9 节、I2 附图片放置规则） | 缓存纪律要求图片不进投影前缀（带图请求前缀与无图会话逐字节一致）；资产外置避免 JSONL 膨胀；尾部放置 + 引用留痕使 I1 重放可完整还原带图请求 |
| 2026-08-29 | 断流重连参数全部内置化：重试上限 5 次、同一 step 连续 429 超 1 次后基础退避 1s→5s（两段式）；移除 2026-08-24 引入的 `retry_max` 配置键，旧配置中的键被忽略 | 撤销上一次「仅次数可调」的让步：次数与曲线是端点无关的耐心策略，实测无需按端点调参，内置常量更符合 7.2 收敛原则；两段式退避把分钟级限流窗口的总耐心提升到约 131s |
| 2026-08-29 | AGENTS.md 注入与 /skill prompt 简化器（新增 6.10 节、internal/skill 包、TUI 弹窗枚举新增 skillPicker）：AGENTS.md 内容会话首拍存入 SessionHeader.agentsMd 并渲染进系统提示词（/new 从磁盘重读）；/skill 唯一命中时把「skill 正文 + 任务」展开为一条 user 消息，无参打开选择器且 Enter 回填不发送 | skill/项目指令定位为 prompt 简化器：明确调用优于自动触发。正文骑 user turn 尾部（I2 明文豁免区），系统提示词除 agentsMd 首拍外零改动、工具目录零改动，I1 自动满足；不做 .agents/rules、hooks、项目级 config（4.3 避开清单与 7.2 收敛原则），模型主动加载留待「反复需要模型自读 skill」的实际需求出现 |
| 2026-08-30 | 回合耗时标记：turn 正常完成后在回答末尾追加一行暗色「（耗时 1m30s）」（TUI 侧 turnStart 起算、覆盖工具环全程；中止/出错回合不打） | 生成中已有思考行与状态栏双计时，但定稿即消失——滚动区缺一个「这轮跑了多久」的持久落款。session 日志逐条带时间戳、重放可推导时长，故纯 TUI 呈现不动 core |
| 2026-09-02 | 当天日期结构化落盘 + 时长渲染统一：新增 internal/human 包统一时长格式（tui/agent/provider/tool 共用）；「今天」由 agent 随每条 user 消息以独立 `date` 字段落盘（UserMessageData.Date），模型投影时展开为消息头部 `Today: YYYY-MM-DD` 前缀（系统提示词恢复纯静态、I2 前缀缓存全程命中；正文无注记，转录回放等人类消费面不泄漏）；回合耗时落款追加本地完成时刻（HH:MM）；等待期 spinner 防静止误判；状态栏丢弃优先级重排（计时器降至与 in/out 同级、平级丢更靠左、晚于 cache）；工具结果投影追加耗时（≥1s）；main 会话 Created 改用真实 UTC 时刻（time.Now().UTC()） | 「今天」须是用户本地当日（UTC 日期在 UTC+8 凌晨差一天），但不能进系统提示词否则前缀缓存失效——date 字段落盘使日期成为固定历史事实、投影时注入使正文保持纯净（拼接进 Text 会把内部注记泄漏给 /resume 转录等所有读者），两者兼得；时长格式三处重复须单一真相；等待期无 spinner 易被误读为卡死；原 Created 拼 "Z" 伪造时区会落盘未来时刻 |
| 2026-09-04 | 明确不做思考控制参数：provider 不传 `reasoning_effort` / thinking 开关类参数，思考强度随端点默认（6.1 关键设计登记翻案条件） | 多家端点实测：各家思考控制参数形态互不兼容（`reasoning_effort` / `thinking.type` / `chat_template_kwargs` / `think`），透传适配成本高且收益不明；档位差异的既有出路是注册多个模型条目走 Ctrl+P（零新增代码，与缓存纪律无冲突） |
| 2026-09-05 | 滚动缓冲区渐进落盘（新增 internal/tui/scroll.go、8.3 改写）：assistant 回答由「定稿一次性 Println」改为「流式期间闭合行超 6 行即落盘 + 定稿补余」；所有落盘统一走 printScroll——宽度归一化（整数倍宽度行补尾空格）、按视口预算分批、超预算巨行硬拆；流中断重连对已落盘部分追加暗色作废标记；用户回显与 slash 输出同通道 | 实机确认长回答定稿时「半截窜到屏顶 + 中部大片空白」：bubbletea v2.0.9 内联渲染器 insertAbove 在打印块折行总行数超视口余量时 CursorUp/InsertLine 被钳制、光标永久失步（main 未修，已知问题类 #1666/#1567），且对「宽度恰为终端宽整数倍」的行多算一行。渐进落盘从源头保证任何一次 Println 都不超视口（打印单位 = 行），宽度归一化对齐估算公式（与 ultraviolet 自身 PrependString 修正公式同公共区），二者都与公式实现解耦、上游修复落地后零改动受益。副产品：流式期间滚动缓冲可搜索可复制。替代方案逐一否决：fork / 自写渲染器——v2 无 WithRenderer 注入点且 renderer 接口方法未导出、包外不可实现；降级 v1——移植 API 成本换另一套内联 artifact；alt-screen 虚拟滚动——击穿 I4；定稿分批 + 预折行补丁——与 insertAbove 内部公式永久耦合，属给缺陷写适配层。上游缺陷按外部事实记录，不提 issue、不跟随其修复节奏（对策与公式实现解耦，上游行为变化不影响正确性）。事件契约登记：MessageFinal.Text 与 TextDelta 累积逐字节一致（agent 定稿处与 8.3 双侧注明），TUI 渐进落盘依赖之。残余风险（tab 宽度、\r、Println 与 ticker flush 交错频率等）入 DEBT 账本 |
