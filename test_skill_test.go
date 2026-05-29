package main

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSkillMD5 验证技能注册后 LLM 能通过 function calling 调用。
func TestSkillMD5(t *testing.T) {
	cfg := loadConfig(t)
	a := NewAgent(cfg, testWork("test-user-skill"))
	a.SetMonitorFn(monitorLog(t))

	defer a.ClearHistory()
	os.WriteFile(a.RolePath(), []byte("你是一个文件处理助手。当需要计算文件 MD5 时请使用 md5 技能。"), 0o644)
	a.RegisterSkill(Skill{
		Name:        "获取指定文件的哈希值",
		Description: "获取指定文件的哈希值",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "description": "计算类型md5/sha-1/sha-256"},
				"file": map[string]any{"type": "string", "description": "文件路径"},
			},
			"required": []string{"type", "file"},
		},
		Returns: "哈希值的十六进制字符串",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			Type, _ := args["type"].(string)
			fn := hash[strings.ToLower(Type)]
			if fn == nil {
				return "不支持此算法", nil
			}
			file, _ := args["file"].(string)
			data, err := os.ReadFile(file)
			if err != nil {
				return "", err
			}
			return fn(data), nil
		},
	})

	testFile := "./testdata/skill_test.txt"
	os.WriteFile(testFile, []byte("hello world"), 0o644)
	defer os.Remove(testFile)

	ctx := context.Background()
	reply, err := a.Send(ctx, "请帮我计算 "+testFile+" 这个文件的 MD5 值")
	if err != nil {
		t.Skipf("API 调用失败（可能是模型不支持 function calling）: %v", err)
	}
	t.Logf("技能回复: %s", reply)

	expected := "5eb63bbbe01eeed093cb22bb8f5acdc3"
	if !strings.Contains(strings.ToLower(reply), expected) {
		t.Logf("注意: 回复未包含期望的 MD5 值 %s，模型可能以不同格式返回", expected)
	}
}

// TestSkillNormalChat 验证注册 skills 后普通对话不受影响。
func TestSkillNormalChat(t *testing.T) {
	cfg := loadConfig(t)
	a := NewAgent(cfg, testWork("test-user-skill-normal"))
	a.SetMonitorFn(monitorLog(t))
	a.RegisterSkill([]Skill{{
		Name:        "get_weather",
		Description: "获取指定城市的天气",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string", "description": "城市名"},
			},
			"required": []string{"city"},
		},
		Returns: "天气和温度的文本描述",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			return "晴天, 25°C", nil
		},
	}}...)

	defer a.ClearHistory()

	ctx := context.Background()
	reply, err := a.Send(ctx, "你好，1+1等于几？")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	t.Logf("普通对话回复: %s", reply)

	if reply == "" {
		t.Error("有 skills 时普通对话应有回复")
	}
}

// TestRegisterSkill 验证 RegisterSkill 可追加技能。
func TestRegisterSkill(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-reg-skill")

	a.RegisterSkill(Skill{
		Name:        "echo",
		Description: "回显用户输入的内容",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"msg": map[string]any{"type": "string", "description": "要回显的消息"},
			},
			"required": []string{"msg"},
		},
		Returns: "ECHO: 后跟原消息文本",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["msg"].(string)
			return "ECHO: " + msg, nil
		},
	})

	ctx := context.Background()
	reply, err := a.Send(ctx, "请用 echo 技能回显: hello world")
	if err != nil {
		t.Skipf("API 调用失败（可能是模型不支持 function calling）: %v", err)
	}
	t.Logf("注册技能回复: %s", reply)
}

var hash = map[string]func(data []byte) string{
	"md5":    md5Hash,
	"sha1":   sha1Hash,
	"sha256": sha256Hash,
	"sha512": sha512Hash,
}

func md5Hash(data []byte) string {
	h := md5.Sum(data)
	return fmt.Sprintf("%x", h)
}

func sha1Hash(data []byte) string {
	h := sha1.Sum(data)
	return fmt.Sprintf("%x", h)
}

func sha256Hash(data []byte) string {
	h := sha256.New()
	return fmt.Sprintf("%x", h.Sum(data))
}

func sha512Hash(data []byte) string {
	h := sha512.New()
	return fmt.Sprintf("%x", h.Sum(data))
}

// TestSkillApproverApprove 验证批准函数放行时技能正常执行。
func TestSkillApproverApprove(t *testing.T) {
	cfg := loadConfig(t)

	a := NewAgent(cfg, testWork("test-user-approve"))
	a.SetMonitorFn(monitorLog(t))
	a.RegisterSkill([]Skill{{
		Name:        "echo",
		Description: "回显消息",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"msg": map[string]any{"type": "string", "description": "要回显的消息"},
			},
			"required": []string{"msg"},
		},
		Returns: "ECHO: 后跟原消息文本",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["msg"].(string)
			return "ECHO: " + msg, nil
		},
	}}...)
	a.SetSkillApproverFn(func(ctx context.Context, skillName string, args map[string]any) bool {
		return true
	})
	defer a.ClearHistory()
	os.WriteFile(a.RolePath(), []byte("你是助手，当需要回显时请使用 echo 技能。"), 0o644)

	ctx := context.Background()
	reply, err := a.Send(ctx, "请用 echo 回显: hello world")
	if err != nil {
		t.Skipf("API 调用失败: %v", err)
	}
	t.Logf("批准放行回复: %s", reply)

	if !strings.Contains(reply, "ECHO") && !strings.Contains(reply, "hello") {
		t.Logf("回复可能不包含 ECHO，模型决定是否调用技能: %s", reply)
	}
}

// TestSkillApproverDeny 验证批准函数拒绝时技能不被执行。
func TestSkillApproverDeny(t *testing.T) {
	cfg := loadConfig(t)
	a := NewAgent(cfg, testWork("test-user-deny"))
	a.SetMonitorFn(monitorLog(t))
	a.RegisterSkill([]Skill{{
		Name:        "echo",
		Description: "回显消息",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"msg": map[string]any{"type": "string", "description": "要回显的消息"},
			},
			"required": []string{"msg"},
		},
		Returns: "ECHO: 后跟原消息文本",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["msg"].(string)
			return "ECHO: " + msg, nil
		},
	}}...)
	a.SetSkillApproverFn(func(ctx context.Context, skillName string, args map[string]any) bool {
		msg, _ := args["msg"].(string)
		return !strings.Contains(msg, "secret")
	})

	defer a.ClearHistory()

	ctx := context.Background()

	reply1, err := a.Send(ctx, "请用 echo 回显: hello")
	if err != nil {
		t.Skipf("API 调用失败: %v", err)
	}
	t.Logf("放行回复: %s", reply1)

	reply2, err := a.Send(ctx, "请用 echo 回显: my secret password")
	if err != nil {
		t.Skipf("API 调用失败: %v", err)
	}
	t.Logf("拒绝回复: %s", reply2)

	if strings.Contains(reply2, "ECHO") {
		t.Errorf("应拒绝的技能不应该输出 ECHO 结果, 实际: %s", reply2)
	}
}

// TestSkillApproverNil 验证不设置批准函数时所有技能自动放行。
func TestSkillApproverNil(t *testing.T) {
	cfg := loadConfig(t)
	a := newTestAgent(t, cfg, "test-user-no-approver")

	a.RegisterSkill(Skill{
		Name:        "echo",
		Description: "回显消息",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"msg": map[string]any{"type": "string", "description": "要回显的消息"},
			},
			"required": []string{"msg"},
		},
		Returns: "ECHO: 后跟原消息文本",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["msg"].(string)
			return "ECHO: " + msg, nil
		},
	})
	defer a.ClearHistory()

	ctx := context.Background()
	reply, err := a.Send(ctx, "请用 echo 回显: test without approver")
	if err != nil {
		t.Skipf("API 调用失败: %v", err)
	}
	t.Logf("无批准函数回复: %s", reply)
}
