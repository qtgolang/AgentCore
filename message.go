// Package ai 提供 AI 对话代理（Agent）核心能力：
//   - 对话上下文持久化管理
//   - 历史消息压缩与摘要
//   - 长期记忆注入
//   - 多模态消息（文本/图片/语音）构造
//   - 用户自定义 LLM 调用（通过 LLMFunc 注入）
//
// 典型用法：上层通过 New(agentConfig{...}) 创建 Agent 实例，
// 调用 Send/SendImage/SendAudio 发送消息，Agent 负责管理上下文并回调
// 用户注入的 LLMFunc 获取回复。
package main

// -------------------------------------------
// 公开类型
// -------------------------------------------

// Message 是传给 LLM API 的单条消息，兼容 OpenAI Chat Completions 格式。
//
// 纯文本：
//
//	{"role": "user", "content": "你好"}
//
// 多模态：
//
//	{"role": "user", "content": [{"type":"text","text":"..."}, {"type":"image_url",...}]}
//
// 函数调用（assistant 发起）：
//
//	{"role": "assistant", "tool_calls": [{"id":"call_1","type":"function","function":{"name":"md5","arguments":"{\"file\":\"a.txt\"}"}}]}
//
// 函数结果（tool 角色回传）：
//
//	{"role": "tool", "tool_call_id": "call_1", "content": "d41d8cd98f00b204e9800998ecf8427e"}
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 表示 LLM 发起的一次函数调用。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数调用的名称和参数（arguments 为 JSON 字符串）。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ContentPart 表示一条多模态消息中的一个部件（text / image_url / input_audio）。
// 直接映射 OpenAI Chat Completions API 的 content 数组元素格式。
//
// 例如图片部件：{"type": "image_url", "image_url": {"url": "...", "detail": "auto"}}
// 语音部件：   {"type": "input_audio", "input_audio": {"data": "<base64>", "format": "mp3"}}
type ContentPart map[string]any

// ContentType 标识消息的内容类型，由 IncomingMessage.ContentType() 返回。
type ContentType string

const (
	ContentTypeText  ContentType = "text"  // 纯文本消息
	ContentTypeImage ContentType = "image" // 含图片消息
	ContentTypeAudio ContentType = "audio" // 含语音消息
)

// UserAgent 是 fetchInputAudioPart 拉取远程语音文件时使用的 HTTP User-Agent 头。
const UserAgent = "Mozilla/5.0"

// IncomingMessage 表示一条来自用户（买家）的入站消息，支持文本/图片/语音三种形态。
//
// 注意：当前重构后 Agent 实例按 userID 绑定，发送消息时直接使用 Send/SendImage/SendAudio，
// 不再需要通过 IncomingMessage 传递 senderID。此类型保留用于未来的多用户场景或外部集成。
type IncomingMessage struct {
	SenderID   string // 发送者唯一标识（如闲鱼买家 ID）
	SenderName string // 发送者显示名称
	Content    string // 纯文本内容（文本消息时使用）
	ImageURL   string // 图片远程地址（图片消息时使用）
	AudioURL   string // 语音远程地址（语音消息时使用）
}

// ContentType 判断消息的实际内容类型。
//
// 判定规则：
//   - ImageURL 非空 → ContentTypeImage
//   - AudioURL 非空 → ContentTypeAudio
//   - 均空         → ContentTypeText
func (m IncomingMessage) ContentType() ContentType {
	if m.ImageURL != "" {
		return ContentTypeImage
	}
	if m.AudioURL != "" {
		return ContentTypeAudio
	}
	return ContentTypeText
}

// -------------------------------------------
// 内部类型
// -------------------------------------------

// chatMsg 是上下文持久化时使用的内部消息格式。
// 与 Message 结构一致，但设计为未导出类型以防止外部直接使用。
// Content 在 JSON 反序列化时保持原始类型（string 或 []any），
// 序列化后可直接写入 OpenAI API 请求体。
type chatMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
