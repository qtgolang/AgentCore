package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadConfig 从 config.json 读取 API 配置，文件不存在或缺少字段时 skip。
func loadConfig(t *testing.T) APIKey {
	t.Helper()
	bs, err := os.ReadFile("config.json")
	if err != nil {
		t.Skipf("跳过: 无法读取 config.json: %v", err)
	}
	var c APIKey
	if err := json.Unmarshal(bs, &c); err != nil {
		t.Skipf("跳过: config.json 解析失败: %v", err)
	}
	if c.Key == "" || c.Model == "" {
		t.Skip("跳过: config.json 缺少 api_key 或 model")
	}
	return c
}

// monitorLog 返回一个 MonitorFunc，将事件输出到 t.Log。
func monitorLog(t *testing.T) MonitorFunc {
	return func(e MonitorEvent) {
		switch e.Type {
		case "tool_call":
			t.Logf("[monitor] %s req=%d skill=%s args=%s result=%s",
				e.Type, e.ReqID, e.ToolName, e.ToolArgs, e.ToolResult)
		case "error":
			t.Logf("[monitor] %s req=%d err=%v", e.Type, e.ReqID, e.Error)
		default:
			t.Logf("[monitor] %s req=%d user=%s content=%s",
				e.Type, e.ReqID, e.UserID, e.Content)
		}
	}
}

// testWork 返回测试用的 Work 配置，所有文件落在 ./testdata/ 下。
func testWork(userID string) Work {
	return Work{
		UserFile: filepath.Join("testdata", "context", userID+".json"),
		Role:     filepath.Join("testdata", "role.md"),
		Mcp:      filepath.Join("testdata", "mcp.json"),
		Memory:   filepath.Join("testdata", "memory.md"),
		Document: filepath.Join("testdata", "document.md"),
	}
}

// newTestAgent 创建一个干净的测试用 Agent（非取消模式）。
func newTestAgent(t *testing.T, cfg APIKey, userID string) *Agent {
	t.Helper()
	a := NewAgent(cfg, testWork(userID))
	a.SetMonitorFn(monitorLog(t))
	a.ClearHistory()
	os.WriteFile(a.RolePath(), []byte("你是一个友好的助手，请用中文简短回复。"), 0o644)
	return a
}

// newCancelAgent 创建一个 CancelOnNew 模式的 Agent。
func newCancelAgent(t *testing.T, cfg APIKey) *Agent {
	t.Helper()
	a := NewAgent(cfg, testWork("test-user-cancel"))
	a.SetMonitorFn(monitorLog(t))
	a.ClearHistory()
	os.WriteFile(a.RolePath(), []byte("你是一个友好的助手，请用中文回复。"), 0o644)
	return a
}
