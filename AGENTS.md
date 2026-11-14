# 给 Agent 的操作指南 — sammal

> 本文件是仓库的**唯一治理文本**（2026-11-14 文档治理起生效，范式取自 dsh-free-pool）：规则进本文件、裁决进 `docs/DECISIONS.md`、规格与不变量进 `docs/SPEC.md`、历史进 git 与 `CHANGELOG.md`。`README.md` 只做项目门面与快速开始。本文件只放稳定规则；本文件同时是 sammal 在本仓库运行时注入系统提示词的项目指令（SPEC 6.10 注入机制），保持简短。

## 0. 文档治理

### 0.1 四终点与僵尸处理

文档只有四个终点：**规则进本文件、裁决进 `docs/DECISIONS.md`、规格与不变量进 `docs/SPEC.md`、历史进 git 与 `CHANGELOG.md`**。长不出终点的文件视为僵尸——冻结即删除（git 即归档，重大删除前按 §0.5 打快照 tag 兜底）。

sammal 既有裁决（2026-11-04）继续有效：历史判读、失败登记与决策过程一律保留原文；过时内容只标注、不删除；无法确认是否过时的标 `⚠️ 待确认`，等待人工裁决，不得擅自处理。

### 0.2 变更路由表

| 变更类型 | 写入目标 | 禁改 |
|---|---|---|
| 不变量 / 模块规格 / 里程碑变更 | `docs/SPEC.md` 对应章节 | 第 2 章 `### I<n> ` 标题格式与连续编号（`internal/specdoc` 契约测试解析它） |
| 设计裁决 / 明确不做项 / 翻案 | `docs/DECISIONS.md` 文末表格追加一行（append-only） | 头部说明与既有条目 |
| 债 / 待验收 / 已知缺陷 | `DEBT.md` 对应行；未完成事项同步「未完成事项总览」 | 已核销行（原文保留） |
| 冒烟用例与结果登记 | `docs/SMOKE.md` | — |
| 迭代 / 发展计划 | 新建 `docs/PLAN-YYYYMMDD.md`；完结后状态改 📌 历史归档 | — |
| 用户可见功能交付 | 代码 + 测试；CHANGELOG 留到 tag 发布时从 git log 与 DECISIONS 编纂 | `CHANGELOG.md` 实时节 |
| 发版 | 打 annotated tag `vX.Y.Z` 并推送 → CI 流水线构建 GitHub Release；`CHANGELOG.md` 补版本节 | 已发布版本节 |
| 文档增删 / 移动 | 本文件 §0.4 文档地图同步一行 | 禁止旁路新建第二份索引 |

### 0.3 写作红线

- **现状文档**（本文件、`README.md`、`DEBT.md` 活行、`docs/SMOKE.md` 现行用例）不写「当前版本号」与版本相关叙事——版本事实只活在 git tag、`CHANGELOG.md` 与二进制（`-version` 打印 goreleaser 注入值；`cmd/sammal/main.go` 的 `version` 变量缺省 `dev`）。
- **历史记录**（DECISIONS 条目、CHANGELOG 版本节、归档文档）允许出现日期与版本，但 append-only：只追加，不改写既有条目。
- **统一状态标记**：

| 标记 | 含义 |
|---|---|
| ✅ 通过 / 已核销 / 已复测通过 | 已完成且有实机或测试证据 |
| ❌ 失败 | 失败登记；历史记录须带日期并注明「保留追溯」 |
| ☐ 未测 | 当前环境未覆盖，须列入关闭结论的未覆盖清单 |
| ⚠️ 待确认 | 无法确认是否过时、是否该删改推，等待人工裁决，不得擅自处理 |
| 📌 归档 | 历史文档或历史表述，原始意图保留，不作为当前事实 |

- **统一写法**：日期 `YYYY-MM-DD`；会话号 `YYYYMMDD-HHMMSS-XXXX`；提交号用 7 位短 hash 并加反引号；仓库内路径用相对路径加反引号；截图存 `docs/image/SMOKE/`，新增文件命名 `wt-<用例>-<时间>-<语义>.png`，历史数字命名文件保持原名（重命名会断链）。未完成事项集中登记在 `DEBT.md`「未完成事项总览」，原文位置保留交叉引用。
- **换行符**：仓库 index 统一 LF（Windows 检出因 `core.autocrlf=true` 呈 CRLF，属本地现象）。不主动做 EOL 整理——统一会产生整文件 diff（2026-11-05 裁决维持现状，见 `DEBT.md` C4）。
- **防腐测试**（`internal/specdoc`，`go test ./internal/specdoc`）：① 每条不变量在 `*_test.go` 有锚点；② 非测试代码禁 `WithAltScreen`（I4）；③ 禁裸 TODO/FIXME（I6）；④ `README.md` / `DEBT.md` / `AGENTS.md` / `CHANGELOG.md` 与 `docs/*.md` 的本地相对链接可解析；⑤ `docs/*.md` 与四个根文档必须登记进本文件 §0.4 文档地图；⑥ 全仓 `.go` ≤ 800 行。

### 0.4 文档地图（全仓唯一索引）

| 文档 | 定位 | 状态 |
|---|---|---|
| `README.md` | 项目门面：是什么、快速开始、不变量速览 | 现行 |
| `AGENTS.md` | 本文件：唯一治理文本（四终点、路由表、红线、文档地图、收尾例程） | 现行 |
| `CHANGELOG.md` | 发布时点产物：tag 维度版本史 | 现行 |
| `DEBT.md` | 显式技术债账本 + 未完成事项总览 | 现行 |
| `docs/SPEC.md` | 开发主文档：七条不变量、模块规格、里程碑 | 现行 |
| `docs/DECISIONS.md` | 设计决策登记簿（append-only 唯一实时台账；2026-11-14 自 SPEC「决策变更记录」整体迁入） | 现行 |
| `docs/SMOKE.md` | 三终端实机冒烟指引与结果登记 | 现行 |
| `docs/PLAN-20261104.md` | 2026-11-04 迭代计划书 | 📌 历史归档（已完结，曾是接手入口） |
| `docs/PLAN-20261111.md` | 2026-11-11 发展计划：M6 立项、调研拍板处置、条件触发生长区 | 现行 |
| `docs/image/` | 截图资产（SMOKE 证据），文件命名见 §0.3 统一写法 | 现行 |

### 0.5 文档删除的快照程序

删除重大历史文档前，先打 annotated tag `docs-snapshot-YYYYMMDD`（git 即归档）；此后正文对被删文档的指称改写为「git tag `docs-snapshot-YYYYMMDD` 快照」，条目文字本身不动。当前未启用：PLAN 归档按既有裁决保留在树内。

## 1. 会话状态与构建（运行时自查，勿假设）

- 工作区根：`D:\github-clone\sammal`；平台 win32 / Git Bash。
- 构建：`go build ./cmd/sammal`；全量回归：`go build ./... && go test ./... -count=1`（推前必跑，见 `DEBT.md` C6）。
- 已知环境性失败：Windows 主机 PATH 首个 bash 为 niubash（MSYS）时，`TestBashTimeout` / `TestBashBackgroundProcess` 两例可红——niubash 对后台孤儿有驻留语义，5s ctx 超时击杀 shell 后叠加 1s WaitDelay 凑出约 6s / exitCode=1 签名；生产 `executeWithJobs` 路径不受影响，语义由 Linux 锁定。基线判定：Windows 列除这两例外全绿即通过（详见 `DEBT.md` 对应行）。
- 版本：`cmd/sammal/main.go` 的 `version` 变量缺省 `dev`，由 goreleaser 以 tag 注入（`.goreleaser.yaml` ldflags）；`.goreleaser.yaml` 的 changelog 过滤 `docs:` / `test:` / `chore:` 前缀提交（仅影响 GitHub release notes，不影响 `CHANGELOG.md`）。
- 发版经 CI：push tag `v*` 触发 `.github/workflows/release.yml`（ubuntu：全量测试 → goreleaser 六目标 GitHub Release）；`GITHUB_TOKEN` 由 CI 注入，本地无需配置。每次 tag 自动产出 Linux 全量回归补证（DEBT 常设项的证据来源）。本地手动出包用 `goreleaser release --snapshot --clean`。

## 2. 里程碑收尾例程

1. **账本同步**：本轮裁决追加 `docs/DECISIONS.md`；债核销 / 补登 `DEBT.md`；规格正文变更同步 `docs/SPEC.md`；实机结果登记 `docs/SMOKE.md`。
2. **全量回归**：`go build ./... && go test ./... -count=1`。
3. **棘轮与纯净检查**：800 行棘轮、禁 alt-screen、禁裸 TODO 由 specdoc 契约自动把守；人工核对命名 / 死代码 / 冗余注释 / 过度设计。
4. **文档地图核对**：新增 / 移动的文档已同步 §0.4（specdoc 契约自动把守）。
