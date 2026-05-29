# AgentCore

Go 嵌入式 AI 对话代理库，零外部依赖，仅 Go 标准库。

核心能力：对话上下文管理、多模态消息、Function Calling（本地技能 + MCP 协议）、历史压缩、长期记忆/知识库注入、多 Provider 支持（OpenAI / Anthropic）。

## 安装

```bash
go get github.com/qtgolang/AgentCore
```

要求 Go 1.20+。

## 快速开始

```go
package main

import (
    "context"
    "fmt"
    agentcore "github.com/qtgolang/AgentCore"
)

func main() {
    agent := agentcore.NewAgent(agentcore.APIKey{
        APIBase: "https://api.openai.com",  // 或兼容 OpenAI 格式的端点
        Key:     "sk-xxx",
        Model:   "gpt-4o",
    }, agentcore.Work{
        UserFile: "./data/user.json",
        Role:     "./data/role.md",
        Mcp:      "./data/mcp.json",
        Memory:   "./data/memory.md",
        Document: "./data/document.md",
    })
    // 可选链式配置
    agent.SetCancelOnNew(true)

    reply, err := agent.Send(context.Background(), "你好，1+1 等于几？")
    if err != nil {
        panic(err)
    }
    fmt.Println(reply)
}
```

所有 Work 路径的文件/目录在首次创建时会自动生成默认模板。

## 消息发送

三种消息形态：

```go
// 纯文本
reply, _ := agent.Send(ctx, "帮我写一首诗")

// 含图片（OpenAI 多模态模型或 Anthropic Claude 3+）
reply, _ := agent.SendImage(ctx, "描述这张图片", "https://example.com/photo.png")

// 含语音（OpenAI 多模态模型，自动拉取远端音频并 Base64 编码）
reply, _ := agent.SendAudio(ctx, "转写这段语音", "https://example.com/audio.mp3")
```

`RecordReply(content)` 可向对话历史手动追加 assistant 消息，适用于人工接管回复后再由 Agent 继续对话的场景。

## Provider 切换

默认使用 OpenAI 兼容格式（`/chat/completions` + Bearer Auth）。切换到 Anthropic 原生格式（`/v1/messages` + x-api-key）：

```go
agent := agentcore.NewAgent(agentcore.APIKey{
    APIBase: "https://api.anthropic.com",
    Key:     "sk-ant-xxx",
    Model:   "claude-sonnet-4-20250514",
}, work).SetProviderAnthropic()
```

切换后 message content 格式、tools 定义、tool calling 多轮交互均自动适配 Anthropic 规范。

也可用 `SetProviderOpenAI()` / `SetProviderAnthropic()` 在运行时切换。

## 技能系统 (Function Calling)

注册本地技能后 LLM 可自动发起调用：

```go
agent.RegisterSkill(agentcore.Skill{
    Name:        "get_weather",
    Description: "查询指定城市的天气",
    Parameters: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "city": map[string]any{"type": "string", "description": "城市名"},
        },
        "required": []string{"city"},
    },
    Returns: "JSON 格式的天气数据",
    Execute: func(ctx context.Context, args map[string]any) (string, error) {
        city := args["city"].(string)
        return fetchWeather(city)
    },
})
```

技能执行流程：LLM 返回 tool_calls → Agent 执行 Execute → 结果回传给 LLM → 继续对话。最多 4096 轮（可通过 `SetToolCallCount(n)` 调整）。

本地 Skill 和 MCP 技能同名时，本地 Skill 优先。

### 技能审批

设置审批回调，在每次技能执行前征求确认：

```go
agent.SetSkillApproverFn(func(ctx context.Context, name string, args map[string]any) bool {
    return name != "delete_all_files" // 拒绝危险操作
})
```

设为 nil 或留空则全部放行。

### 技能文档

在 role.md 同级目录下创建 `skills/` 文件夹，放入 `*.md` 文件，内容会自动注入 system message 的【技能文档】部分。

## MCP 协议 (Model Context Protocol)

支持 JSON-RPC 2.0 over HTTP/SSE，在 `mcp.json` 中配置服务器：

```json
{
  "mcpServers": {
    "my-tools": { "url": "http://localhost:8080/mcp" }
  }
}
```

运行时管理（纯内存，不落盘）：

```go
agent.EnableMCP("my-tools")                   // 握手 + 拉取工具列表
agent.IsMCPEnabled("my-tools")                // 查询状态
agent.DisableMCP("my-tools")                  // 禁用整个服务器

agent.EnableMCPTool("my-tools", "search_docs") // 启用单个工具
agent.DisableMCPTool("my-tools", "delete_doc") // 禁用单个工具

servers := agent.MCPServers()                 // 所有服务器及工具状态
skills, _ := agent.MCPSkills("my-tools")      // 单个服务器的工具状态
```

MCP 状态存储于 Agent 实例内存中，不同 Agent 实例独立。

## 对话管理

### 上下文持久化

对话历史存储在 `Work.UserFile` 指定的 JSON 文件中，自动加载、追加、落盘。`ClearHistory()` 删除上下文文件。

### 历史压缩

当实际 token 用量接近上限时自动触发：保留最近 6 条消息，更早的消息通过 LLM 生成中文摘要，以 `【历史对话摘要】` 前缀注入。可通过摘要精度保持上下文在 token 预算内。

可通过 `SetMaxContextTokens(n)` 调整触发阈值（默认 65535），越小越早触发压缩。

### System Message 装配

每次对话时 system message 按以下顺序拼接：
1. 【角色设定】— `role.md`
2. 【长期记忆】— `memory.md`
3. 【知识库】— `document.md`
4. 【技能文档】— `skills/*.md`

这四个文件在 Agent 运行时持续可编辑，每次对话自动重新读取。

### 长期记忆与知识库

```go
// 获取路径，供外部读写
path := agent.MemoryPath()   // memory.md
path := agent.DocumentPath() // document.md
path := agent.RolePath()     // role.md
```

文件内容持久化在磁盘上，多个 Agent 实例共享同一文件即可共享知识。

## 监控回调

通过 `SetMonitorFn` 注入监控函数，所有关键事件会触发回调：

```go
agent.SetMonitorFn(func(e agentcore.MonitorEvent) {
    switch e.Type {
    case "request":    // 用户发送消息
    case "reply":      // AI 回复
    case "tool_call":  // 技能执行（含参数和结果）
    case "error":      // 错误
    case "cancelled":  // 请求被取消
    }
})
```

## 取消模式

`SetCancelOnNew(true)` 开启后，当前正在进行的 API 请求会在新请求到达时被取消，旧请求返回 `CancelRequest` 错误。适用于多线程并发场景（如聊天界面快速连续发送）。

```go
agent.SetCancelOnNew(true)

// 线程 1：耗时较长的请求
go agent.Send(ctx, "写一篇 5000 字的小说")

// 线程 2：用户又发了一条新消息，自动取消线程 1
reply, _ := agent.Send(ctx, "算了，1+1 等于几？")
```

## HTTP 客户端配置

通过 `SetHTTPClient` 注入自定义 HTTP 客户端，用于代理、自定义 TLS、超时等场景（默认 600s 超时）：

```go
agent.SetHTTPClient(&http.Client{
    Timeout: 120 * time.Second,
    Transport: &http.Transport{
        Proxy: http.ProxyURL(proxyURL),
    },
})
```

## 更多公开方法

| 方法 | 说明 |
|------|------|
| `Send(ctx, text) (string, error)` | 发送纯文本消息 |
| `SendImage(ctx, text, imageURL) (string, error)` | 发送含图片消息 |
| `SendAudio(ctx, text, audioURL) (string, error)` | 发送含语音消息 |
| `RecordReply(content) error` | 向历史追 assistant 消息 |
| `ClearHistory() error` | 删除上下文文件 |
| `RegisterSkill(s ...Skill)` | 注册本地技能 |
| `SetCancelOnNew(bool) *Agent` | 取消模式开关 |
| `SetToolCallCount(n int) *Agent` | 工具调用最大轮数（默认 4096） |
| `SetMaxContextTokens(n int) *Agent` | 上下文 token 上限（默认 65535） |
| `SetHTTPClient(client *http.Client) *Agent` | 注入自定义 HTTP 客户端 |
| `SetProviderOpenAI() *Agent` | 切换 OpenAI Provider |
| `SetProviderAnthropic() *Agent` | 切换 Anthropic Provider |
| `SetSkillApproverFn(fn SkillApprover) *Agent` | 技能审批回调 |
| `SetMonitorFn(fn MonitorFunc) *Agent` | 监控回调 |
| MCP 方法 | 见 MCP 协议章节 |

## License

MIT