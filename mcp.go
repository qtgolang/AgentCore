package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// -------------------------------------------
// MCP 公开类型
// -------------------------------------------

// MCPManifest 表示工作目录下 mcp.json 的解析结果。
type MCPManifest struct {
	Servers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig 单个 MCP 服务器的连接配置。
type MCPServerConfig struct {
	URL string `json:"url"`
}

// MCPTool 从 MCP 服务器 tools/list 返回的工具/技能定义。
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPServerStatus 返回 MCP 服务器及其工具的状态摘要。
type MCPServerStatus struct {
	Name    string           `json:"name"`
	URL     string           `json:"url"`
	Enabled bool             `json:"enabled"`
	Tools   int              `json:"tools"`
	Skills  []MCPSkillStatus `json:"skills"`
}

// MCPSkillStatus 返回 MCP 工具及其启用状态。
type MCPSkillStatus struct {
	MCPTool
	Enabled bool `json:"enabled"`
}

// -------------------------------------------
// 内部类型：Agent 内存中的 MCP 状态
// -------------------------------------------

// mcpServerInfo Agent 内部维护的单个 MCP 服务器完整状态（纯内存，不落盘）。
type mcpServerInfo struct {
	URL     string
	Enabled bool
	Tools   []mcpToolEntry
}

// mcpToolEntry 单个工具条目，嵌入 MCPTool 并附加禁用标记。
type mcpToolEntry struct {
	MCPTool
	Disabled bool
}

// -------------------------------------------
// JSON-RPC 内部类型
// -------------------------------------------

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      *int   `json:"id,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
	ID      int             `json:"id"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// -------------------------------------------
// 初始化：仅从 mcp.json 读取服务器列表
// -------------------------------------------

// initMCP 在 New 时调用，从指定路径加载服务器列表到内存。
// 文件不存在时自动创建空清单。
func (a *Agent) initMCP(mcpPath string) {
	a.mcpServers = map[string]*mcpServerInfo{}
	if mcpPath == "" {
		return
	}
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		_ = os.MkdirAll(filepath.Dir(mcpPath), 0o755)
		_ = os.WriteFile(mcpPath, []byte("{\n  \"mcpServers\": {}\n}\n"), 0o644)
		return
	}
	var m MCPManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	for name, cfg := range m.Servers {
		a.mcpServers[name] = &mcpServerInfo{URL: cfg.URL}
	}
}

// -------------------------------------------
// 查询方法
// -------------------------------------------

// MCPServers 返回所有 MCP 服务器及其技能的启用/禁用状态。
func (a *Agent) MCPServers() []MCPServerStatus {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()

	var out []MCPServerStatus
	for name, info := range a.mcpServers {
		s := MCPServerStatus{
			Name:    name,
			URL:     info.URL,
			Enabled: info.Enabled,
			Tools:   len(info.Tools),
		}
		for _, t := range info.Tools {
			s.Skills = append(s.Skills, MCPSkillStatus{
				MCPTool: t.MCPTool,
				Enabled: info.Enabled && !t.Disabled,
			})
		}
		out = append(out, s)
	}
	return out
}

// MCPSkills 返回指定 MCP 下所有技能的启用/禁用状态。
func (a *Agent) MCPSkills(mcpName string) ([]MCPSkillStatus, error) {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()

	info, ok := a.mcpServers[mcpName]
	if !ok {
		return nil, fmt.Errorf("MCP %q 不存在", mcpName)
	}
	if len(info.Tools) == 0 {
		return nil, fmt.Errorf("MCP %q 尚未抓取技能", mcpName)
	}

	var out []MCPSkillStatus
	for _, t := range info.Tools {
		out = append(out, MCPSkillStatus{
			MCPTool: t.MCPTool,
			Enabled: info.Enabled && !t.Disabled,
		})
	}
	return out, nil
}

// -------------------------------------------
// 启用/禁用：MCP 级别（纯内存）
// -------------------------------------------

// EnableMCP 启用指定 MCP：从服务器拉取技能列表，存到内存。
func (a *Agent) EnableMCP(mcpName string) error {
	a.mcpMu.RLock()
	info, ok := a.mcpServers[mcpName]
	a.mcpMu.RUnlock()
	if !ok {
		return fmt.Errorf("MCP %q 未在 mcp.json 中定义", mcpName)
	}

	tools, err := a.listMCPTools(info.URL)
	if err != nil {
		return fmt.Errorf("启用 MCP %s 失败: %w", mcpName, err)
	}

	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	info, ok = a.mcpServers[mcpName]
	if !ok {
		return fmt.Errorf("MCP %q 不存在", mcpName)
	}
	info.Enabled = true
	info.Tools = make([]mcpToolEntry, len(tools))
	for i, t := range tools {
		info.Tools[i] = mcpToolEntry{MCPTool: t}
	}
	return nil
}

// DisableMCP 禁用指定 MCP（仅标记，不删除工具数据）。
func (a *Agent) DisableMCP(mcpName string) error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	info, ok := a.mcpServers[mcpName]
	if !ok {
		return fmt.Errorf("MCP %q 不存在", mcpName)
	}
	info.Enabled = false
	return nil
}

// IsMCPEnabled 查询 MCP 是否启用。
func (a *Agent) IsMCPEnabled(mcpName string) bool {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()
	info, ok := a.mcpServers[mcpName]
	return ok && info.Enabled
}

// -------------------------------------------
// 启用/禁用：工具级别（纯内存）
// -------------------------------------------

// EnableMCPTool 启用指定 MCP 下的单个技能。
func (a *Agent) EnableMCPTool(mcpName, toolName string) error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	info, ok := a.mcpServers[mcpName]
	if !ok {
		return fmt.Errorf("MCP %q 不存在", mcpName)
	}
	for i := range info.Tools {
		if info.Tools[i].Name == toolName {
			info.Tools[i].Disabled = false
			return nil
		}
	}
	return fmt.Errorf("MCP %q 中没有技能 %q", mcpName, toolName)
}

// DisableMCPTool 禁用指定 MCP 下的单个技能。
func (a *Agent) DisableMCPTool(mcpName, toolName string) error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	info, ok := a.mcpServers[mcpName]
	if !ok {
		return fmt.Errorf("MCP %q 不存在", mcpName)
	}
	for i := range info.Tools {
		if info.Tools[i].Name == toolName {
			info.Tools[i].Disabled = true
			return nil
		}
	}
	return fmt.Errorf("MCP %q 中没有技能 %q", mcpName, toolName)
}

// IsMCPToolEnabled 查询指定技能是否可用（MCP 启用且该技能未被禁）。
func (a *Agent) IsMCPToolEnabled(mcpName, toolName string) bool {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()

	info, ok := a.mcpServers[mcpName]
	if !ok || !info.Enabled {
		return false
	}
	for _, t := range info.Tools {
		if t.Name == toolName {
			return !t.Disabled
		}
	}
	return false
}

// -------------------------------------------
// MCP 协议客户端（JSON-RPC over HTTP/SSE）
// -------------------------------------------

const mcpAcceptHeader = "application/json, text/event-stream"

// listMCPTools 通过 MCP 初始化握手 + tools/list 获取工具列表。
func (a *Agent) listMCPTools(serverURL string) ([]MCPTool, error) {
	if _, err := a.mcpCall(serverURL, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "SunnyNetAgentCore",
			"version": "1.0.0",
		},
	}); err != nil {
		return nil, fmt.Errorf("initialize 失败: %w", err)
	}

	_ = a.mcpNotify(serverURL, "notifications/initialized", nil)

	result, err := a.mcpCall(serverURL, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("tools/list 失败: %w", err)
	}

	var listResult struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &listResult); err != nil {
		return nil, fmt.Errorf("解析工具列表失败: %w", err)
	}
	return listResult.Tools, nil
}

// mcpCall 发送 JSON-RPC 请求并返回 result 字段的原始 JSON。
func (a *Agent) mcpCall(serverURL string, method string, params any) (json.RawMessage, error) {
	id := 1
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      &id,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", mcpAcceptHeader)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	body = parseSSEData(respBody, resp.Header.Get("Content-Type"))

	var jr jsonrpcResponse
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("JSON-RPC 解析失败: %w", err)
	}
	if jr.Error != nil {
		return nil, fmt.Errorf("JSON-RPC 错误 (code=%d): %s", jr.Error.Code, jr.Error.Message)
	}
	return jr.Result, nil
}

// mcpNotify 发送 JSON-RPC 通知（无 id，不期待响应）。
func (a *Agent) mcpNotify(serverURL string, method string, params any) error {
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", mcpAcceptHeader)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// parseSSEData 从 SSE 格式响应中提取 data 字段内容。
func parseSSEData(raw []byte, contentType string) []byte {
	if !strings.Contains(contentType, "text/event-stream") {
		return raw
	}
	var buf strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			buf.WriteString(strings.TrimSpace(rest))
		}
	}
	if buf.Len() > 0 {
		return []byte(buf.String())
	}
	return raw
}

// findMCPTool 在已启用的 MCP 服务器中查找指定工具，返回服务器 URL。
func (a *Agent) findMCPTool(toolName string) (serverURL string, ok bool) {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()
	for _, info := range a.mcpServers {
		if !info.Enabled {
			continue
		}
		for _, t := range info.Tools {
			if t.Name == toolName && !t.Disabled {
				return info.URL, true
			}
		}
	}
	return "", false
}

// mcpToolCall 调用 MCP 服务器的 tools/call 并返回文本结果。
func (a *Agent) mcpToolCall(ctx context.Context, serverURL, toolName string, args map[string]any) (string, error) {
	result, err := a.mcpCall(serverURL, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}

	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &callResult); err != nil {
		return string(result), nil
	}
	var texts []string
	for _, c := range callResult.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			texts = append(texts, c.Text)
		}
	}
	out := strings.Join(texts, "\n")
	if callResult.IsError {
		return out, fmt.Errorf("%s", out)
	}
	return out, nil
}
