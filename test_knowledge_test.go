package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestKnowledgeBase 验证长期记忆内容能影响 LLM 回复。
func TestKnowledgeBase(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-kb")
	ctx := context.Background()
	defer a.ClearHistory()

	kbPath := a.MemoryPath()
	t.Logf("长期记忆路径: %s", kbPath)

	// 写入长期记忆
	content := "退换货政策：本店支持7天无理由退货。质量问题包来回运费，非质量问题买家承担退货运费。尺码问题属于非质量问题。"
	if err := os.WriteFile(kbPath, []byte(content), 0o644); err != nil {
		t.Fatalf("写入记忆失败: %v", err)
	}
	//defer os.WriteFile(kbPath, []byte(""), 0o644) // 清理

	reply, err := a.Send(ctx, "我的长期记忆里有退换货政策吗？")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	t.Logf("长期记忆回复: %s", reply)

	if !strings.Contains(reply, "7天") || !strings.Contains(reply, "无理由") {
		t.Errorf("回复应包含长期记忆中的退换货政策内容, 实际: %s", reply)
	}
}

// TestKnowledgeBaseEmpty 验证长期记忆为空时不影响正常对话。
func TestKnowledgeBaseEmpty(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-kb-empty")
	ctx := context.Background()
	defer a.ClearHistory()

	// 确保长期记忆为空
	kbPath := a.MemoryPath()
	os.WriteFile(kbPath, []byte(""), 0o644)

	reply, err := a.Send(ctx, "1+1等于几？")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	t.Logf("空记忆回复: %s", reply)

	if reply == "" {
		t.Error("空记忆时应有正常回复")
	}
}

// TestKnowledgeBasePath 验证 MemoryPath 和 DocumentPath 返回有效路径。
func TestKnowledgeBasePath(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-kb-path")

	path := a.MemoryPath()
	if path == "" {
		t.Error("MemoryPath 不应返回空字符串")
	}
	if !strings.Contains(path, "memory.md") {
		t.Errorf("MemoryPath 应包含 memory.md, 实际: %s", path)
	}
	t.Logf("MemoryPath: %s", path)

	docPath := a.DocumentPath()
	if docPath == "" {
		t.Error("DocumentPath 不应返回空字符串")
	}
	if !strings.Contains(docPath, "document.md") {
		t.Errorf("DocumentPath 应包含 document.md, 实际: %s", docPath)
	}
	t.Logf("DocumentPath: %s", docPath)

	rolePath := a.RolePath()
	if rolePath == "" {
		t.Error("RolePath 不应返回空字符串")
	}
	if !strings.Contains(rolePath, "role.md") {
		t.Errorf("RolePath 应包含 role.md, 实际: %s", rolePath)
	}
	t.Logf("RolePath: %s", rolePath)
}
