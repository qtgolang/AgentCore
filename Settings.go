// Package main — AgentCore: AI 对话代理嵌入式库。
//
// Settings.go 提供 Agent 实例的链式配置方法（Builder 模式），
// 所有 Set* 方法均返回 *Agent 自身以支持级联调用。
package main

import "net/http"

// SetToolCallCount 设置 Function Calling 的最大循环轮数。
//
// 当 LLM 连续返回 tool_calls 时，Agent 会循环执行技能并将结果回传，
// 直到 LLM 返回纯文本回复或达到此上限。默认值 4096。
//
// 示例：
//
//	agent.SetToolCallCount(10) // 最多 10 轮工具调用后强制终止
func (a *Agent) SetToolCallCount(count int) *Agent {
	a.maxToolCallRounds = count
	return a
}

// SetProviderOpenAI 将 LLM 提供商切换为 OpenAI 兼容格式。
//
// 使用 POST {APIBase}/chat/completions，Authorization: Bearer {Key} 认证。
// 这是 NewAgent 的默认行为，无需显式调用，仅在运行时从 Anthropic 切回时使用。
//
// 示例：
//
//	agent.SetProviderAnthropic()  // 先切到 Anthropic
//	// ... 一些操作 ...
//	agent.SetProviderOpenAI()     // 再切回 OpenAI
func (a *Agent) SetProviderOpenAI() *Agent {
	a.isOpenAiProvider = true
	return a
}

// SetProviderAnthropic 将 LLM 提供商切换为 Anthropic 原生格式。
//
// 使用 POST {APIBase}/v1/messages，x-api-key: {Key} 认证，
// 自动处理 system prompt 提取到顶层字段、content 格式转换、
// tools 定义适配（function → input_schema）、tool 结果合并为单条 user 消息等差异。
//
// 调用前请确保 APIKey.APIBase 指向 Anthropic 端点（如 https://api.anthropic.com），
// APIKey.Key 为 Anthropic API Key（sk-ant- 前缀）。
//
// 示例：
//
//	agent := NewAgent(APIKey{
//	    APIBase: "https://api.anthropic.com",
//	    Key:     "sk-ant-xxx",
//	    Model:   "claude-sonnet-4-20250514",
//	}, work).SetProviderAnthropic()
func (a *Agent) SetProviderAnthropic() *Agent {
	a.isOpenAiProvider = false
	return a
}

// SetSkillApproverFn 设置技能执行前审批回调。
//
// 当 LLM 发起 function calling 时，Agent 在执行前调用 fn。
// fn 接收 ctx、技能名和已解析的参数 map，返回 true 表示批准执行，
// 返回 false 表示拒绝（Agent 会将拒绝原因回传给 LLM）。
//
// 传入 nil 则清除审批回调，所有技能调用自动放行（默认行为）。
//
// 示例：
//
//	agent.SetSkillApproverFn(func(ctx context.Context, name string, args map[string]any) bool {
//	    if name == "delete_file" {
//	        return false // 拒绝危险操作
//	    }
//	    return true
//	})
func (a *Agent) SetSkillApproverFn(fn SkillApprover) *Agent {
	a.skillApprover = fn
	return a
}

// SetMonitorFn 设置全局监控回调。
//
// fn 在以下事件发生时被调用（在 Agent 内部同步触发）：
//   - "request"   — 用户发送消息后
//   - "reply"     — LLM 返回完整回复后
//   - "tool_call" — 技能执行完成后（含参数和结果）
//   - "error"     — 发生错误时
//   - "cancelled" — 请求被取消模式终止时
//
// 传入 nil 则关闭监控（默认行为）。
//
// 示例：
//
//	agent.SetMonitorFn(func(e MonitorEvent) {
//	    log.Printf("[%s] req=%d content=%s", e.Type, e.ReqID, e.Content)
//	})
func (a *Agent) SetMonitorFn(fn MonitorFunc) *Agent {
	a.monitor = fn
	return a
}

// SetCancelOnNew 设置并发请求取消模式。
//
// 开启后：当 Agent 正在进行一次 Send 调用（等待 LLM 回复）时，
// 若有新的 Send 调用到达，旧的请求会被 context 取消，
// 旧调用返回 CancelRequest 错误（"客户重新发起请求,当前请求被取消"）。
//
// 关闭后（默认）：多个 Send 调用串行等待——后到的请求在前一个完成后才执行。
//
// 典型场景：聊天界面中用户快速连续发送消息，只关心最新回复。
//
// 示例：
//
//	agent.SetCancelOnNew(true)
//
//	// 线程 1：发送耗时请求
//	go agent.Send(ctx, "写一篇长文章")
//
//	// 线程 2：用户又发了一条新消息，线程 1 自动取消
//	reply, _ := agent.Send(ctx, "1+1=几？")
func (a *Agent) SetCancelOnNew(open bool) *Agent {
	a.cancelOnNew = open
	return a
}

// SetMaxContextTokens 设置上下文 token 上限。
//
// 当上一轮 API 响应的实际 token 用量超过 (上限 - 当前消息估算 token)
// 且差值不低于 4096 时，自动触发历史压缩：保留最近 6 条消息，
// 更早的消息通过 LLM 生成中文摘要注入上下文。
//
// 默认值 65535。设为 0 或负数时保持默认值。
//
// 示例：
//
//	agent.SetMaxContextTokens(32768) // 更激进的压缩阈值
func (a *Agent) SetMaxContextTokens(n int) *Agent {
	if n > 0 {
		a.maxContextTokens = n
	}
	return a
}

// SetHTTPClient 注入自定义 HTTP 客户端，替代 NewAgent 创建的默认客户端。
//
// 默认客户端：&http.Client{Timeout: 600 * time.Second}。
//
// 典型用途：
//   - 设置代理：http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
//   - 自定义 TLS 证书配置
//   - 注入 http.RoundTripper 中间件（日志、重试、限流）
//
// 示例：
//
//	agent.SetHTTPClient(&http.Client{
//	    Timeout: 120 * time.Second,
//	    Transport: &http.Transport{
//	        Proxy: http.ProxyURL(proxyURL),
//	    },
//	})
func (a *Agent) SetHTTPClient(client *http.Client) *Agent {
	if client != nil {
		a.httpClient = client
	}
	return a
}