package tool

// 默认六件套的注册顺序：即 Defs() 的序列化顺序，会话内不可变（I2）。
// 新增工具必须同时登记于此与 Resolve 的 switch，否则默认配置会缺工具。
var defaultToolNames = []string{"read", "write", "edit", "bash", "grep", "glob"}

// Resolve 按配置的工具名列表实例化工具；names 为空时回退默认六件套。
// 返回顺序与 names 一致，保证 I2 序列化确定；未知工具名静默跳过。
// shell 只在实例化 bash 工具时消费（BashTool 用它定格降级语义）。
func Resolve(cwd, shell string, names []string) []Tool {
	if len(names) == 0 {
		names = defaultToolNames
	}
	tools := make([]Tool, 0, len(names))
	for _, n := range names {
		switch n {
		case "read":
			tools = append(tools, &ReadTool{WorkDir: cwd})
		case "write":
			tools = append(tools, &WriteTool{WorkDir: cwd})
		case "edit":
			tools = append(tools, &EditTool{WorkDir: cwd})
		case "bash":
			tools = append(tools, &BashTool{WorkDir: cwd, Shell: shell})
		case "grep":
			tools = append(tools, &GrepTool{WorkDir: cwd})
		case "glob":
			tools = append(tools, &GlobTool{WorkDir: cwd})
		}
	}
	return tools
}
