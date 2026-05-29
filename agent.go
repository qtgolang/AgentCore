package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const minTokensForCompaction = 4096

// SkillApprover 技能批准函数。
// 当 LLM 触发技能调用时，Agent 会在执行前调用此函数征求批准。
// 返回 true 表示批准执行，返回 false 表示拒绝，技能不会被调用。
// 若为 nil，则所有技能调用自动放行。
type SkillApprover func(ctx context.Context, skillName string, args map[string]any) bool

// MonitorEvent 描述一次 Agent 事件，供 MonitorFunc 消费。
type MonitorEvent struct {
	Type       string    // "request" | "reply" | "tool_call" | "error" | "cancelled"
	UserID     string    // 用户 ID
	ReqID      int64     // 请求序号
	Content    string    // 用户输入或 AI 回复文本
	ToolName   string    // tool_call 时的技能名
	ToolArgs   string    // tool_call 时的参数 JSON
	ToolResult string    // tool_call 时的执行结果
	Error      error     // error 时的错误
	Timestamp  time.Time // 事件时间
}

// MonitorFunc 监控回调。Agent 在所有关键事件发生时调用，无返回值。
// 若为 nil 则不触发任何监控调用。
type MonitorFunc func(MonitorEvent)

type APIKey struct {
	APIBase string `json:"api_base"`
	Key     string `json:"api_key"`
	Model   string `json:"model"`
}
type Work struct {
	UserFile string //context 会话上下文文件、全路径,例如[./aaa/bbb/bbb/user.json]如果 路径/文件 不存在则自动创建
	Role     string //角色设定 role.md文件 全路径,例如[./aaa/bbb/bbb/role.md]如果 路径/文件 不存在则自动创建 # 角色设定：在这里定义 AI 助手的身份、语气、行为准则等。
	Mcp      string //mcp.json 从 mcp.json 加载服务器列表到内存。 全路径,例如[./aaa/bbb/bbb/mcp.json]如果 路径/文件 不存在则自动创建
	Memory   string // memory.md 长期记忆文件 全路径,例如[./aaa/bbb/bbb/memory.md]如果 路径/文件 不存在则自动创建  # 长期记忆：在这里记录用户偏好、历史决策、个人信息等。
	Document string // document.md 知识库文件 全路径,例如[./aaa/bbb/bbb/document.md]如果 路径/文件 不存在则自动创建  # 知识库：在这里填写项目文档、API 说明、业务规则等。
}

// Agent 对话代理核心。
type Agent struct {
	APIKey
	httpClient        *http.Client              // HTTP 客户端
	store             *contextStore             // 上下文持久化
	skills            map[string]Skill          // 已注册的技能
	skillApprover     SkillApprover             // 技能批准回调
	monitor           MonitorFunc               // 监控回调
	maxContextTokens  int                       // 上下文 token 上限
	mu                sync.Mutex                // 并发保护
	cancelOnNew       bool                      // 取消模式开关
	cancelMu          sync.Mutex                // 取消函数保护
	cancelFunc        context.CancelFunc        // 当前请求的取消函数
	cancelReqID       int64                     // 当前 cancelFunc 所属请求 ID
	reqSeq            int64                     // 请求序号（原子递增）
	mcpServers        map[string]*mcpServerInfo // MCP 服务器内存状态
	mcpMu             sync.RWMutex              // MCP 状态读写锁
	runningTokens     int                       // 上次 API 响应的实际 token 数
	isOpenAiProvider  bool                      // LLM 提供商类型："openai" | "anthropic"
	maxToolCallRounds int                       //工具调用最大次数 默认4096
}

// NewAgent 创建 Agent 实例。
func NewAgent(key APIKey, work Work) *Agent {
	a := &Agent{
		APIKey:            key,
		httpClient:        &http.Client{Timeout: 600 * time.Second},
		skills:            make(map[string]Skill),
		skillApprover:     nil,
		monitor:           nil,
		cancelOnNew:       false,
		isOpenAiProvider:  true,
		maxToolCallRounds: 4096,
	}
	if a.maxContextTokens <= 0 {
		a.maxContextTokens = 65535
	}
	a.store = newContextStore(work)
	a.initMCP(work.Mcp)
	return a
}

// RegisterSkill 注册一个技能。同名技能会被覆盖。
func (a *Agent) RegisterSkill(s ...Skill) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, skill := range s {
		a.skills[skill.Name] = skill
	}

}

// emit 触发监控回调（monitor 为 nil 时无操作）。
func (a *Agent) emit(e MonitorEvent) {
	if a.monitor != nil {
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		a.monitor(e)
	}
}

// Send 发送纯文本用户消息并返回 LLM 回复。
func (a *Agent) Send(ctx context.Context, text string) (string, error) {
	return a.sendInternal(ctx, chatMsg{Role: "user", Content: text})
}

// SendImage 发送包含图片的用户消息。
func (a *Agent) SendImage(ctx context.Context, text, imageURL string) (string, error) {
	return a.sendInternal(ctx, chatMsg{Role: "user", Content: imageParts(text, imageURL)})
}

// SendAudio 发送包含语音的用户消息。
func (a *Agent) SendAudio(ctx context.Context, text, audioURL string) (string, error) {
	return a.sendInternal(ctx, chatMsg{Role: "user", Content: audioParts(text, audioURL)})
}

// RecordReply 向对话历史追加一条 assistant 角色消息。
func (a *Agent) RecordReply(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.append(chatMsg{Role: "assistant", Content: content})
}

// ClearHistory 删除当前用户的对话上下文文件。
func (a *Agent) ClearHistory() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.clear()
}

// MemoryPath 返回长期记忆文件的路径。
func (a *Agent) MemoryPath() string {
	return a.store.MemoryPath()
}

// DocumentPath 返回知识库文档的路径。
func (a *Agent) DocumentPath() string {
	return a.store.DocumentPath()
}

// RolePath 返回角色文档的路径。
func (a *Agent) RolePath() string {
	return a.store.RolePath()
}

// chatResponse LLM API 返回的解析结果。
type chatResponse struct {
	Content     string     // 纯文本回复（无 tool_calls 时有值）
	ToolCalls   []ToolCall // 函数调用列表（LLM 想要调用技能时有值）
	TotalTokens int        // API 返回的实际 prompt+completion token 数
	FullMessage *Message   // 完整的 assistant 消息（Anthropic 场景，含 content 和 tool_use）
}

// sendInternal 核心对话流程。
//
// 流程：
//  1. 持锁完成状态准备（加载历史、压缩、存储用户消息、组装消息列表），然后释放锁
//  2. 若 CancelOnNew 开启，取消前一个请求并创建带 cancel 的子 ctx
//  3. 调用 runChat 执行 API 循环（不持锁）
//  4. 持锁保存 assistant 回复
func (a *Agent) sendInternal(ctx context.Context, userMsg chatMsg) (string, error) {
	myReqID := atomic.AddInt64(&a.reqSeq, 1)

	// Phase 1: 状态准备（持锁）
	a.mu.Lock()
	history := a.store.load()
	estimated := len(fmt.Sprintf("%v", userMsg.Content)) / 2
	if estimated >= a.maxContextTokens {
		a.mu.Unlock()
		return "", fmt.Errorf("消息过长，估算 token(%d) 超过上限(%d)", estimated, a.maxContextTokens)
	}
	v := a.maxContextTokens - estimated
	if a.runningTokens >= v && v >= minTokensForCompaction {
		compacted, err := a.compactHistory(ctx, history)
		if err != nil {
			a.mu.Unlock()
			return "", fmt.Errorf("对话摘要失败: %w", err)
		}
		history = compacted
		a.runningTokens = 0
	}
	a.store.append(userMsg)
	messages := a.buildMessages(history, userMsg)
	a.mu.Unlock()

	// 监控: 请求事件
	userContent := extractContent(userMsg)
	a.emit(MonitorEvent{Type: "request", ReqID: myReqID, Content: userContent})

	// Phase 2: 取消管理
	if a.cancelOnNew {
		a.cancelMu.Lock()
		if a.cancelFunc != nil {
			a.cancelFunc()
		}
		ctx, a.cancelFunc = context.WithCancel(ctx)
		a.cancelReqID = myReqID
		a.cancelMu.Unlock()
	}

	// Phase 3: API 循环（不持锁）
	reply, totalTokens, err := a.runChat(ctx, messages)

	// Phase 4: 清理取消函数（仅当仍是自己的 cancelFunc）
	if a.cancelOnNew {
		a.cancelMu.Lock()
		if a.cancelReqID == myReqID {
			a.cancelFunc = nil
			a.cancelReqID = 0
		}
		a.cancelMu.Unlock()
	}

	if err != nil {
		if a.cancelOnNew && errors.Is(err, context.Canceled) {
			//a.emit(MonitorEvent{Type: "cancelled", ReqID: myReqID, Content: "重复请求"})
			return "", CancelRequest
		}
		a.emit(MonitorEvent{Type: "error", ReqID: myReqID, Error: err})
		return "", err
	}

	// 监控: 回复事件
	a.emit(MonitorEvent{Type: "reply", ReqID: myReqID, Content: reply})

	// Phase 5: 保存 assistant 回复（持锁）
	a.mu.Lock()
	a.store.append(chatMsg{Role: "assistant", Content: reply})
	a.runningTokens = totalTokens
	a.mu.Unlock()

	return reply, nil
}

var CancelRequest = errors.New("客户重新发起请求,当前请求被取消. ")

// runChat 执行 API 调用循环（含 function calling 多轮），返回回复文本及 API 报告的实际 token 数。
// 调用方不应持锁。
func (a *Agent) runChat(ctx context.Context, messages []Message) (string, int, error) {
	for round := 0; round < a.maxToolCallRounds; round++ {
		resp, err := a.chatCompletion(ctx, messages, 0.7, 0)
		if err != nil {
			return "", 0, err
		}
		if len(resp.ToolCalls) == 0 {
			return resp.Content, resp.TotalTokens, nil
		}
		// Append assistant message: FullMessage for Anthropic (contains tool_use in content array),
		// standard ToolCalls-based message for OpenAI.
		if resp.FullMessage != nil {
			messages = append(messages, *resp.FullMessage)
		} else {
			messages = append(messages, Message{Role: "assistant", ToolCalls: resp.ToolCalls})
		}
		// Execute tool calls and append results. Anthropic merges all results into one user message.
		if !a.isOpenAiProvider {
			toolParts := make([]ContentPart, 0, len(resp.ToolCalls))
			for _, tc := range resp.ToolCalls {
				result := a.executeToolCall(ctx, tc)
				toolParts = append(toolParts, ContentPart{
					"type":        "tool_result",
					"tool_use_id": tc.ID,
					"content":     result,
				})
			}
			messages = append(messages, Message{Role: "user", Content: toolParts})
		} else {
			for _, tc := range resp.ToolCalls {
				result := a.executeToolCall(ctx, tc)
				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    result,
				})
			}
		}
	}
	return "", 0, fmt.Errorf("工具调用超过最大轮次 %d", a.maxToolCallRounds)
}

// executeToolCall 执行单个工具调用并返回结果文本。
// 优先匹配本地 Skill，未命中则尝试已启用的 MCP 技能。
func (a *Agent) executeToolCall(ctx context.Context, tc ToolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("错误：无法解析参数: %v", err)
	}

	// 1. 本地 Skill
	skill, ok := a.skills[tc.Function.Name]
	if ok {
		if a.skillApprover != nil && !a.skillApprover(ctx, tc.Function.Name, args) {
			return fmt.Sprintf("未批准执行技能 %s", tc.Function.Name)
		}
		result, err := skill.Execute(ctx, args)
		if err != nil {
			return fmt.Sprintf("错误：执行失败: %v", err)
		}
		if skill.Returns != "" {
			result = result + "\n---\n返回值说明: " + skill.Returns
		}
		a.emit(MonitorEvent{
			Type:       "tool_call",
			ToolName:   tc.Function.Name,
			ToolArgs:   tc.Function.Arguments,
			ToolResult: result,
		})
		return result
	}

	// 2. MCP 技能
	mcpURL, found := a.findMCPTool(tc.Function.Name)
	if !found {
		return fmt.Sprintf("错误：未知技能 %s", tc.Function.Name)
	}
	result, err := a.mcpToolCall(ctx, mcpURL, tc.Function.Name, args)
	if err != nil {
		return fmt.Sprintf("错误：MCP 调用失败: %v", err)
	}
	a.emit(MonitorEvent{
		Type:       "tool_call",
		ToolName:   tc.Function.Name,
		ToolArgs:   tc.Function.Arguments,
		ToolResult: result,
	})
	return result
}

// extractContent 从 chatMsg 中提取可读文本，用于监控日志。
func extractContent(m chatMsg) string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []ContentPart:
		for _, p := range v {
			if t, _ := p["text"].(string); t != "" {
				return t
			}
		}
		return "(非文本内容)"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// buildMessages 组装完整消息列表：system(含长期记忆+知识库) + 历史 + 当前用户消息。
func (a *Agent) buildMessages(history []chatMsg, current chatMsg) []Message {
	mem := a.store.loadMemory()
	doc := a.store.loadDocument()
	role := a.store.loadRole()
	skills := a.loadSkillDocs()
	sysContent := buildSystemContent(role, mem, doc, skills)

	out := make([]Message, 0, 1+len(history)+1)
	out = append(out, Message{Role: "system", Content: sysContent})
	for _, m := range history {
		out = append(out, Message{Role: m.Role, Content: m.Content})
	}
	out = append(out, Message{Role: current.Role, Content: current.Content})
	return out
}

// loadSkillDocs 读取 Role 文件同目录下的 skills/*.md 全部内容并拼接返回。
func (a *Agent) loadSkillDocs() string {
	dir := filepath.Join(a.store.RoleDir(), "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var buf strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(content)
	}
	return buf.String()
}

// buildTools 将已注册技能和已启用的 MCP 技能合并为 OpenAI tools 参数。
// 本地 Skill 优先于同名 MCP 技能。
func (a *Agent) buildTools() []map[string]any {
	total := len(a.skills)
	a.mcpMu.RLock()
	for _, info := range a.mcpServers {
		if info.Enabled {
			for _, t := range info.Tools {
				if !t.Disabled {
					total++
				}
			}
		}
	}
	a.mcpMu.RUnlock()
	if total == 0 {
		return nil
	}

	tools := make([]map[string]any, 0, total)
	seen := map[string]bool{}

	// 本地 Skill 优先
	for _, s := range a.skills {
		tools = append(tools, s.buildToolDef())
		seen[s.Name] = true
	}

	// 已启用的 MCP 技能（排除与本地 Skill 同名的）
	a.mcpMu.RLock()
	for _, info := range a.mcpServers {
		if !info.Enabled {
			continue
		}
		for _, t := range info.Tools {
			if t.Disabled || seen[t.Name] {
				continue
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
	}
	a.mcpMu.RUnlock()

	return tools
}

// chatCompletion 根据 ProviderType 分发给对应的 LLM 提供商实现。
func (a *Agent) chatCompletion(ctx context.Context, messages []Message, temperature float64, maxTokens int) (*chatResponse, error) {
	if !a.isOpenAiProvider {
		return a.chatCompletionAnthropic(ctx, messages, temperature, maxTokens)
	}
	return a.chatCompletionOpenAI(ctx, messages, temperature, maxTokens)
}

// chatCompletionOpenAI 调用 OpenAI 兼容 API，返回聊天回复或工具调用。
func (a *Agent) chatCompletionOpenAI(ctx context.Context, messages []Message, temperature float64, maxTokens int) (*chatResponse, error) {
	base := strings.TrimRight(a.APIBase, "/")
	url := base + "/chat/completions"

	payload := map[string]any{
		"model":       a.Model,
		"messages":    messages,
		"temperature": temperature,
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	if tools := a.buildTools(); len(tools) > 0 {
		payload["tools"] = tools
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.APIKey.Key)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("AI API 错误 (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []ToolCall      `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("AI 返回为空")
	}

	msg := result.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		return &chatResponse{ToolCalls: msg.ToolCalls, TotalTokens: result.Usage.TotalTokens}, nil
	}
	content, err := parseContent(msg.Content)
	if err != nil {
		return nil, err
	}
	return &chatResponse{Content: content, TotalTokens: result.Usage.TotalTokens}, nil
}

// parseContent 解析 OpenAI content 字段（string 或 []ContentPart 数组）。
func parseContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("AI 返回 content 为空")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s), nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("无法解析 AI 返回 content: %w", err)
	}
	var b strings.Builder
	for _, p := range parts {
		if strings.TrimSpace(p.Text) != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// ---------- 多模态消息构造 ----------

const maxAudioFetchBytes = 2 << 20

func imageParts(text, imageURL string) []ContentPart {
	imageURL = strings.TrimSpace(imageURL)
	text = strings.TrimSpace(text)
	parts := []ContentPart{{"type": "text", "text": text}}
	if imageURL != "" {
		parts = append(parts, ContentPart{
			"type":      "image_url",
			"image_url": map[string]string{"url": imageURL, "detail": "auto"},
		})
	}
	return parts
}

func audioParts(text, audioURL string) []ContentPart {
	audioURL = strings.TrimSpace(audioURL)
	text = strings.TrimSpace(text)
	parts := []ContentPart{{"type": "text", "text": text}}
	if audioURL == "" {
		return parts
	}
	if audioPart, ok := fetchInputAudioPart(audioURL); ok {
		parts = append(parts, audioPart)
	} else {
		parts[0]["text"] = text + "\n语音链接: " + audioURL
	}
	return parts
}

func fetchInputAudioPart(audioURL string) (ContentPart, bool) {
	client := &http.Client{Timeout: 12 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, audioURL, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioFetchBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxAudioFetchBytes {
		return nil, false
	}
	format := audioFormatFromURL(audioURL, resp.Header.Get("Content-Type"))
	return ContentPart{
		"type": "input_audio",
		"input_audio": map[string]string{
			"data":   base64.StdEncoding.EncodeToString(body),
			"format": format,
		},
	}, true
}

func audioFormatFromURL(rawURL, contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return "mp3"
	case strings.Contains(ct, "wav"):
		return "wav"
	case strings.Contains(ct, "ogg"):
		return "ogg"
	case strings.Contains(ct, "webm"):
		return "webm"
	}
	lower := strings.ToLower(rawURL)
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		return "mp3"
	case strings.HasSuffix(lower, ".wav"):
		return "wav"
	case strings.HasSuffix(lower, ".ogg"):
		return "ogg"
	case strings.HasSuffix(lower, ".webm"):
		return "webm"
	default:
		return "mp3"
	}
}
