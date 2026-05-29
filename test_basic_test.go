package main

import (
	"context"
	"strings"
	"testing"
)

// TestBasicSend 验证单轮纯文本对话。
func TestBasicSend(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-basic")
	a.SetProviderAnthropic()
	ctx := context.Background()
	defer a.ClearHistory()

	reply, err := a.Send(ctx, "你好，请用一句话介绍你自己。")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if reply == "" {
		t.Error("回复为空")
	}
	t.Logf("回复: %s", reply)
}

// TestMultiRound 验证多轮对话能正确引用上下文中已提到的信息。
func TestMultiRound(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-multi")
	ctx := context.Background()
	defer a.ClearHistory()

	// 第一轮: 告诉一个信息
	_, err := a.Send(ctx, "1+1=几？")
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}

	// 第二轮: 追问之前的信息
	reply, err := a.Send(ctx, "再加一呢")
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	t.Logf("多轮回复: %s", reply)

	// 第二轮: 追问之前的信息
	reply, err = a.Send(ctx, "再加8呢")
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	t.Logf("多轮回复: %s", reply)

}

// TestManualReply 验证人工插入回复后 LLM 能正确衔接。
func TestManualReply(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-manual")
	ctx := context.Background()
	defer a.ClearHistory()

	// 用户消息
	_, err := a.Send(ctx, "我想了解一下退换货政策")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}

	// 人工回复
	if err := a.RecordReply("亲，质量问题我们承担来回运费。"); err != nil {
		t.Fatalf("RecordReply 失败: %v", err)
	}

	// 追问
	reply, err := a.Send(ctx, "那如果是尺码不合适呢？")
	if err != nil {
		t.Fatalf("追问失败: %v", err)
	}
	t.Logf("人工回复后追问: %s", reply)
}

// TestContextPersistence 验证关闭后新建同名 Agent 能加载之前的对话历史。
func TestContextPersistence(t *testing.T) {
	cfg := loadConfig(t)
	userID := "test-user-persist"

	// 第一个 Agent 发送消息
	a1 := newTestAgent(t, cfg, userID)
	ctx := context.Background()

	_, err := a1.Send(ctx, "记住: 我的订单号是 ORDER-12345")
	if err != nil {
		t.Fatalf("a1.Send 失败: %v", err)
	}

	// 新建同名 Agent 验证历史加载
	a2 := newTestAgent(t, cfg, userID) // 不清空历史
	reply, err := a2.Send(ctx, "我的订单号是什么？")
	if err != nil {
		t.Fatalf("a2.Send 失败: %v", err)
	}
	t.Logf("持久化验证回复: %s", reply)

	if !strings.Contains(reply, "ORDER-12345") {
		t.Errorf("第二个 Agent 应能回忆订单号 ORDER-12345, 实际: %s", reply)
	}

	a2.ClearHistory()
}
