package main

import (
	"context"
	"testing"
)

// TestSendImage 验证发送图片消息。
func TestSendImage(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-image")
	ctx := context.Background()
	defer a.ClearHistory()

	// 使用一张真实可访问的图片 URL
	imageURL := "https://gips3.baidu.com/it/u=3886271102,3123389489&fm=3028&app=3028&f=JPEG&fmt=auto?w=1280&h=960"
	reply, err := a.SendImage(ctx, "请分析这张图片的内容", imageURL)
	if err != nil {
		t.Skipf("SendImage API 失败: %v", err)
	}
	t.Logf("图片分析回复: %s", reply)

	if reply == "" {
		t.Error("图片消息回复不应为空")
	}
}

// TestSendImageEmptyText 验证纯图片无文本的情况。
func TestSendImageEmptyText(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-image2")
	ctx := context.Background()
	defer a.ClearHistory()

	imageURL := "https://gips3.baidu.com/it/u=3886271102,3123389489&fm=3028&app=3028&f=JPEG&fmt=auto?w=1280&h=960"
	reply, err := a.SendImage(ctx, "", imageURL)
	if err != nil {
		t.Skipf("SendImage API 失败: %v", err)
	}
	t.Logf("纯图片回复: %s", reply)

	if reply == "" {
		t.Error("纯图片回复不应为空")
	}
}

// TestSendImageEmptyURL 验证空 URL 时退化为纯文本消息不掉命。
func TestSendImageEmptyURL(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-image3")
	ctx := context.Background()
	defer a.ClearHistory()

	reply, err := a.SendImage(ctx, "你好", "")
	if err != nil {
		t.Skipf("SendImage 失败: %v", err)
	}
	t.Logf("空URL图片回复: %s", reply)

	if reply == "" {
		t.Error("空URL时应有文本回复")
	}
}

// TestSendAudio 验证发送语音消息（需要音频 URL 对目标模型可用）。
func TestSendAudio(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-audio")
	ctx := context.Background()
	defer a.ClearHistory()

	// 发送空 URL → 退回纯文本
	reply, err := a.SendAudio(ctx, "请回复这条消息", "")
	if err != nil {
		t.Fatalf("SendAudio 失败: %v", err)
	}
	t.Logf("音频(空URL)回复: %s", reply)

	if reply == "" {
		t.Error("SendAudio 回复不应为空")
	}
}
