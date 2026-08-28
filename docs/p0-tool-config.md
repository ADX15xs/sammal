# P0 交接：工具集从 main.go 硬编码迁移到配置文件

## 背景

`sammal` 的六件套工具（read / write / edit / bash / grep / glob）在 `cmd/sammal/main.go` 中硬编码组装，无法通过配置切换工具子集。用户无法定制一个"只含 read + write"的轻量子智能体。

参考外部讨论中的架构思路：平台层（通用 agent 循环 + 工具注册表）与业务层（子智能体 + 工具绑定）应当解耦，工具集应由配置驱动而非代码驱动。

## 改动范围

仅涉及两个文件，不触碰 agent 核心、provider、session、tool 实现。

## 方案

### 1. `internal/config/config.go`

在 `Config` 结构体上增加 `Tools` 字段；空值由 `tool.Resolve` 回退默认六件套（向后兼容），无需命名类型。

```go
type Config struct {
    DefaultModel string           `toml:"default_model"`
    Models       map[string]Model `toml:"models"`
    UI           UI               `toml:"ui"`
    Tools        []string         `toml:"tools"` // 新增；空 = 默认六件套
}
```

### 2. `cmd/sammal/main.go`

将 `:87-94` 的硬编码替换为按配置筛选的组装逻辑：

```go
// 现状（需删除）
registry := tool.NewRegistry(
    &tool.ReadTool{WorkDir: cwd},
    &tool.WriteTool{WorkDir: cwd},
    &tool.EditTool{WorkDir: cwd},
    &tool.BashTool{WorkDir: cwd, Shell: facts.Shell},
    &tool.GrepTool{WorkDir: cwd},
    &tool.GlobTool{WorkDir: cwd},
)

// 目标
tools := tool.Resolve(cwd, facts.Shell, cfg.Tools)
registry := tool.NewRegistry(tools...)
```

`tool.Resolve` 是一个纯函数（位于 `internal/tool/registry.go`，新建），负责把工具名列表映射为 `Tool` 实例：

```go
// tool.Resolve 根据 cwd/shell 实例化工具并返回 tool.Tool 切片。
// names 为空时返回全部六件套（默认行为）；非空时只返回命中的工具。
func Resolve(cwd, shell string, names []string) []Tool {
    if len(names) == 0 {
        names = allToolNames // ["read","write","edit","bash","grep","glob"]
    }
    var out []Tool
    for _, n := range names {
        switch n {
        case "read":
            out = append(out, &ReadTool{WorkDir: cwd})
        case "write":
            out = append(out, &WriteTool{WorkDir: cwd})
        // ... 其余四个
        default:
            // 未知工具名静默跳过（不报错）；如需严格校验可改为 emit warning
        }
    }
    return out
}
```

### 3. `config.toml` 示例（用户侧用法）

```toml
# 默认六件套（不填 tools 字段时行为不变）
tools = []

# 只保留读写文件的子智能体
tools = ["read", "write", "edit"]
```

## 不变量影响评估

| 不变量 | 影响 | 说明 |
|--------|------|------|
| I1 模型可见 = 已写入日志 | 无 | 工具执行逻辑不变，日志写入路径不变 |
| I2 请求前缀字节级稳定 | 无 | 工具 schema 仍静态；`tool.Resolve` 在会话创建时调用，`bash` 的 shellNote 仍定格于会话首拍 |
| I3 会话可从日志完整重建 | 无 | 日志结构不变 |
| I4 单渲染路径 | 无 | 不涉及 UI |
| I5 工具输出与渲染分离 | 无 | `Result`/`ForModel`/`ForTUI` 不变 |
| I6 零隐性债 | 无 | — |

## 测试要求

1. **`config_test.go`**：新增 `Tools` 字段的解析测试（缺省、空列表、非空子集）。
2. **`tool/registry_test.go`**（新建）：`Resolve` 的 golden 测试——给定 `names` 与 `cwd`/`shell`，断言返回的工具名顺序与预期完全一致（顺序对 I2 至关重要）。
3. **`main_test.go`**：`TestToolRegistryFromConfig` 直接测装配函数 `toolRegistry(cfg, cwd, shell)`——缺省时 `Defs()` 返回六件套且顺序与 schema 合法，`tools=["read","write"]` 时仅返回两个定义。

## 实现顺序建议

1. 先改 `config.go`（加字段 + 测试）
2. 再建 `tool/registry.go` 与 `tool/registry_test.go`（`Resolve` 函数）
3. 最后改 `main.go` 的组装点（3 行替换）
4. 跑 `go test ./...` 全量验证

## 未覆盖的后续（不在 P0 范围内）

- P1：系统提示词拆分为平台段 + 业务段（需新建 `prompt.go` 模板机制）
- P2：分层工具注册（`platformReg.With(businessTools...)`）
- P3：`.sammalrc` 多子智能体配置格式
