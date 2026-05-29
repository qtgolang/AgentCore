package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const anthropicDefaultMaxTokens = 4096
const anthropicAPIVersion = "2023-06-01"

// chatCompletionAnthropic 调用 Anthropic Messages API (/v1/messages)，返回统一的 chatResponse。
func (a *Agent) chatCompletionAnthropic(ctx context.Context, messages []Message, temperature float64, maxTokens int) (*chatResponse, error) {
	// 分离 system prompt
	systemContent, chatMessages := extractAnthropicSystem(messages)

	// 转换 messages
	anthropicMsgs, err := convertToAnthropicMessages(chatMessages)
	if err != nil {
		return nil, err
	}

	// 转换 tools
	anthropicTools := convertToAnthropicTools(a.buildTools())

	payload := map[string]any{
		"model":      a.Model,
		"max_tokens": maxTokens,
		"messages":   anthropicMsgs,
	}
	if temperature > 0 {
		payload["temperature"] = temperature
	}
	if systemContent != "" {
		payload["system"] = systemContent
	}
	if len(anthropicTools) > 0 {
		payload["tools"] = anthropicTools
	}
	if maxTokens <= 0 {
		payload["max_tokens"] = anthropicDefaultMaxTokens
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := buildAnthropicURL(a.APIBase)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey.Key)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("AI API 错误 (%d): %s", resp.StatusCode, string(body))
	}

	return parseAnthropicResponse(body)
}

// buildAnthropicURL 构造 Anthropic API 端点。
func buildAnthropicURL(base string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/messages"
}

// extractAnthropicSystem 从 messages 中分离 system 消息，返回 system 文本和剩余消息。
func extractAnthropicSystem(messages []Message) (string, []Message) {
	var systemParts []string
	rest := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			s := contentToString(m.Content)
			if s != "" {
				systemParts = append(systemParts, s)
			}
		} else {
			rest = append(rest, m)
		}
	}
	return strings.Join(systemParts, "\n\n"), rest
}

// contentToString 将 Message.Content 转为纯文本字符串。
func contentToString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []ContentPart:
		var b strings.Builder
		for _, p := range v {
			if t, ok := p["text"].(string); ok {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(t)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// convertToAnthropicMessages 将内部 Message 列表转为 Anthropic 消息格式。
func convertToAnthropicMessages(messages []Message) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		content, err := convertToAnthropicContent(m)
		if err != nil {
			return nil, err
		}
		if content == nil {
			continue
		}
		result = append(result, map[string]any{
			"role":    m.Role,
			"content": content,
		})
	}
	return result, nil
}

// convertToAnthropicContent 将单条 Message 的 Content 转为 Anthropic content 数组。
func convertToAnthropicContent(m Message) (any, error) {
	switch v := m.Content.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "text", "text": v}}, nil
	case []ContentPart:
		parts := make([]map[string]any, 0, len(v))
		for _, p := range v {
			converted := convertContentPart(p)
			if converted != nil {
				parts = append(parts, converted)
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return parts, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("不支持的消息内容类型: %T", m.Content)
	}
}

// convertContentPart 将单个 ContentPart 转为 Anthropic 格式。
func convertContentPart(p ContentPart) map[string]any {
	switch p["type"] {
	case "text":
		text, _ := p["text"].(string)
		if text == "" {
			return nil
		}
		return map[string]any{"type": "text", "text": text}
	case "image_url":
		img, ok := p["image_url"].(map[string]any)
		if !ok {
			if imgStr, ok := p["image_url"].(map[string]string); ok {
				if url, ok := imgStr["url"]; ok && url != "" {
					return map[string]any{
						"type": "image",
						"source": map[string]any{
							"type": "url",
							"url":  url,
						},
					}
				}
			}
			return nil
		}
		url, _ := img["url"].(string)
		if url == "" {
			return nil
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "url",
				"url":  url,
			},
		}
	default:
		// 跳过不支持的部件（如 input_audio）
		return nil
	}
}

// convertToAnthropicTools 将 OpenAI 格式 tools 转为 Anthropic 格式。
func convertToAnthropicTools(openaiTools []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(openaiTools))
	for _, t := range openaiTools {
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		at := map[string]any{
			"name":        fn["name"],
			"description": fn["description"],
		}
		if params, ok := fn["parameters"]; ok {
			at["input_schema"] = params
		}
		result = append(result, at)
	}
	return result
}

// parseAnthropicResponse 解析 Anthropic Messages API 响应。
func parseAnthropicResponse(body []byte) (*chatResponse, error) {
	var raw struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Content) == 0 {
		return nil, fmt.Errorf("AI 返回 content 为空")
	}

	var textParts []string
	var toolCalls []ToolCall

	for _, c := range raw.Content {
		switch c.Type {
		case "text":
			if t := strings.TrimSpace(c.Text); t != "" {
				textParts = append(textParts, t)
			}
		case "tool_use":
			args, _ := json.Marshal(c.Input)
			toolCalls = append(toolCalls, ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      c.Name,
					Arguments: string(args),
				},
			})
		}
	}

	content := strings.Join(textParts, "\n")
	totalTokens := raw.Usage.InputTokens + raw.Usage.OutputTokens

	if len(toolCalls) > 0 {
		// 构造 FullMessage：保留 Anthropic 原始 content 格式，供 runChat 追加到消息列表
		fullContent := make([]ContentPart, 0, len(raw.Content))
		for _, c := range raw.Content {
			switch c.Type {
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					fullContent = append(fullContent, ContentPart{"type": "text", "text": c.Text})
				}
			case "tool_use":
				var input map[string]any
				json.Unmarshal(c.Input, &input)
				fullContent = append(fullContent, ContentPart{
					"type":  "tool_use",
					"id":    c.ID,
					"name":  c.Name,
					"input": input,
				})
			}
		}
		return &chatResponse{
			Content:     content,
			ToolCalls:   toolCalls,
			TotalTokens: totalTokens,
			FullMessage: &Message{Role: "assistant", Content: fullContent},
		}, nil
	}

	return &chatResponse{
		Content:     content,
		TotalTokens: totalTokens,
	}, nil
}
