package main

import (
	"context"
	"os"
	"testing"
)

func testMCPAgent(t *testing.T) *Agent {
	t.Helper()
	a := NewAgent(loadConfig(t), testWork("test-user-mcp"))
	a.SetMonitorFn(monitorLog(t))

	os.WriteFile(a.RolePath(), []byte("test"), 0o644)
	return a
}

func firstMCPServer(t *testing.T, agent *Agent) string {
	t.Helper()
	servers := agent.MCPServers()
	if len(servers) == 0 {
		t.Skip("mcp.json 中没有配置任何服务器")
	}
	return servers[0].Name
}

func TestMCPEnableDisable(t *testing.T) {
	agent := testMCPAgent(t)
	name := firstMCPServer(t, agent)

	agent.DisableMCP(name)

	// 启用
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("EnableMCP 失败: %v", err)
	}
	if !agent.IsMCPEnabled(name) {
		t.Fatal("EnableMCP 后应为启用状态")
	}

	// MCPSkills 应有内容
	skills, err := agent.MCPSkills(name)
	if err != nil {
		t.Fatalf("MCPSkills 失败: %v", err)
	}
	t.Logf("%s 共有 %d 个技能", name, len(skills))
	if len(skills) == 0 {
		t.Fatal("技能列表不应为空")
	}

	// 禁用
	if err := agent.DisableMCP(name); err != nil {
		t.Fatalf("DisableMCP 失败: %v", err)
	}
	if agent.IsMCPEnabled(name) {
		t.Fatal("DisableMCP 后应为禁用状态")
	}

	// MCPSkills 仍可调用但技能全部不可用
	skills, _ = agent.MCPSkills(name)
	for _, s := range skills {
		if s.Enabled {
			t.Errorf("禁用后技能 %s 应为不可用", s.Name)
		}
	}
}

func TestMCPReEnable(t *testing.T) {
	agent := testMCPAgent(t)
	name := firstMCPServer(t, agent)

	agent.DisableMCP(name)

	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("首次 EnableMCP 失败: %v", err)
	}
	if err := agent.DisableMCP(name); err != nil {
		t.Fatalf("首次 DisableMCP 失败: %v", err)
	}
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("二次 EnableMCP 失败: %v", err)
	}
	if !agent.IsMCPEnabled(name) {
		t.Fatal("二次启用后应为启用状态")
	}

	skills, err := agent.MCPSkills(name)
	if err != nil {
		t.Fatalf("MCPSkills 失败: %v", err)
	}
	for _, s := range skills {
		if !s.Enabled {
			t.Errorf("二次启用后技能 %s 应为可用", s.Name)
		}
	}
}

func TestMCPToolEnableDisable(t *testing.T) {
	agent := testMCPAgent(t)
	name := firstMCPServer(t, agent)

	agent.DisableMCP(name)
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("EnableMCP 失败: %v", err)
	}

	skills, err := agent.MCPSkills(name)
	if err != nil {
		t.Fatalf("MCPSkills 失败: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("技能列表不应为空")
	}
	toolName := skills[0].Name

	// 默认可用
	if !agent.IsMCPToolEnabled(name, toolName) {
		t.Fatal("技能默认应可用")
	}

	// 禁用单个技能
	if err := agent.DisableMCPTool(name, toolName); err != nil {
		t.Fatalf("DisableMCPTool 失败: %v", err)
	}
	if agent.IsMCPToolEnabled(name, toolName) {
		t.Fatal("禁用后应不可用")
	}

	// 重新启用
	if err := agent.EnableMCPTool(name, toolName); err != nil {
		t.Fatalf("EnableMCPTool 失败: %v", err)
	}
	if !agent.IsMCPToolEnabled(name, toolName) {
		t.Fatal("重新启用后应可用")
	}

	// 禁用整个 MCP：所有技能不可用
	if err := agent.DisableMCP(name); err != nil {
		t.Fatalf("DisableMCP 失败: %v", err)
	}
	if agent.IsMCPToolEnabled(name, toolName) {
		t.Fatal("MCP 禁用后技能应不可用")
	}

	// 重新启用 MCP：工具级禁用应被清除
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("二次 EnableMCP 失败: %v", err)
	}
	if !agent.IsMCPToolEnabled(name, toolName) {
		t.Fatal("二次启用后技能应恢复可用")
	}
}

func TestMCPQueryMethods(t *testing.T) {
	agent := testMCPAgent(t)
	name := firstMCPServer(t, agent)

	agent.DisableMCP(name)
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("EnableMCP 失败: %v", err)
	}

	// MCPServers 返回列表
	servers := agent.MCPServers()
	if len(servers) == 0 {
		t.Fatal("MCPServers 不应为空")
	}

	found := false
	for _, s := range servers {
		if s.Name != name {
			continue
		}
		found = true
		if !s.Enabled {
			t.Error("MCPServers 中 Enabled 应为 true")
		}
		if s.Tools <= 0 {
			t.Error("MCPServers 中 Tools 应大于 0")
		}
		if len(s.Skills) != s.Tools {
			t.Fatalf("Skills 数量(%d) 与 Tools(%d) 不一致", len(s.Skills), s.Tools)
		}
		for _, sk := range s.Skills {
			if !sk.Enabled {
				t.Errorf("技能 %s 应为启用状态", sk.Name)
			}
		}
		break
	}
	if !found {
		t.Fatal("MCPServers 中未找到目标 MCP")
	}

	// MCPSkills
	skills, err := agent.MCPSkills(name)
	if err != nil {
		t.Fatalf("MCPSkills 失败: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("MCPSkills 不应为空")
	}

	// 禁用后查询
	agent.DisableMCP(name)
	servers = agent.MCPServers()
	for _, s := range servers {
		if s.Name == name {
			for _, sk := range s.Skills {
				if sk.Enabled {
					t.Errorf("禁用后技能 %s 应为不可用", sk.Name)
				}
			}
		}
	}

	// 新 Agent 实例不继承状态（纯内存）
	agent2 := testMCPAgent(t)
	if agent2.IsMCPEnabled(name) {
		t.Error("新 Agent 不应继承旧 Agent 的启用状态")
	}
}

func TestMCPTest(t *testing.T) {
	agent := testMCPAgent(t)
	name := firstMCPServer(t, agent)

	agent.DisableMCP(name)
	if err := agent.EnableMCP(name); err != nil {
		t.Fatalf("EnableMCP 失败: %v", err)
	}

	ctx := context.Background()
	skills, err := agent.MCPSkills(name)
	if err != nil {
		t.Fatalf("MCPSkills 失败: %v", err)
	}
	for _, s := range skills {
		agent.EnableMCPTool(name, s.Name)
	}
	reply, err := agent.Send(ctx, "抓包工具当前有多少记录")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if reply == "" {
		t.Error("回复为空")
	}
	t.Logf("回复: %s", reply)

	reply, err = agent.Send(ctx, "第5条是什么类型的数据，主机名是什么？")
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if reply == "" {
		t.Error("回复为空")
	}
	t.Logf("回复: %s", reply)
}
