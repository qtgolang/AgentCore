package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const keepRecentAfterSummary = 6
const summaryPrefix = "【历史对话摘要】"

// compactHistory 对超过上限的历史消息做摘要压缩，将早期消息替换为一段摘要文本。
func (a *Agent) compactHistory(ctx context.Context, history []chatMsg) ([]chatMsg, error) {
	keep := keepRecentAfterSummary
	if keep >= len(history) {
		keep = len(history) / 2
		if keep < 2 {
			keep = 2
		}
	}
	toSummarize := history[:len(history)-keep]
	recent := history[len(history)-keep:]

	transcript := formatHistoryTranscript(toSummarize)
	if strings.TrimSpace(transcript) == "" {
		return history, nil
	}

	summary, err := a.summarizeTranscript(ctx, transcript)
	if err != nil {
		return nil, err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, fmt.Errorf("对话摘要为空")
	}

	compact := []chatMsg{{
		Role:    "user",
		Content: summaryPrefix + "\n" + summary + "\n\n（以上为较早对话的摘要；请结合下方最近消息继续回复。）",
	}}
	compact = append(compact, recent...)
	if err := a.store.replace(compact); err != nil {
		return nil, err
	}
	return compact, nil
}

// summarizeTranscript 调用 chatCompletion 生成对话摘要。
func (a *Agent) summarizeTranscript(ctx context.Context, transcript string) (string, error) {
	messages := []Message{
		{
			Role: "system",
			Content: "你是对话摘要助手。请将以下对话压缩为简洁中文摘要，" +
				"保留：关键讨论点、用户请求、承诺、待办事项。不要编造。控制在 1000 字以内。",
		},
		{Role: "user", Content: "请总结以下对话记录：\n\n" + transcript},
	}
	resp, err := a.chatCompletion(ctx, messages, 0.2, 1200)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func formatHistoryTranscript(msgs []chatMsg) string {
	var b strings.Builder
	for i, m := range msgs {
		role := strings.TrimSpace(m.Role)
		switch role {
		case "user":
			role = "用户"
		case "assistant":
			role = "助手"
		default:
			role = "系统"
		}
		text := strings.TrimSpace(chatMsgToPlainText(m))
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, summaryPrefix) {
			text = strings.TrimSpace(strings.TrimPrefix(text, summaryPrefix))
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%d] %s: %s", i+1, role, text)
	}
	return b.String()
}

func chatMsgToPlainText(m chatMsg) string {
	return contentToPlainText(m.Content)
}

func contentToPlainText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []ContentPart:
		return partsToPlainText(v)
	case []any:
		parts := make([]ContentPart, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				parts = append(parts, ContentPart(m))
			}
		}
		return partsToPlainText(parts)
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}

func partsToPlainText(parts []ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		switch p["type"] {
		case "text":
			if t, _ := p["text"].(string); strings.TrimSpace(t) != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(t)
			}
		case "image_url":
			if u := extractImageURL(p); u != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString("[图片:" + u + "]")
			}
		case "input_audio":
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString("[语音消息]")
		}
	}
	return b.String()
}

func extractImageURL(p ContentPart) string {
	if img, ok := p["image_url"].(map[string]any); ok {
		if u, _ := img["url"].(string); u != "" {
			return u
		}
	}
	if img, ok := p["image_url"].(map[string]string); ok {
		return img["url"]
	}
	return ""
}
