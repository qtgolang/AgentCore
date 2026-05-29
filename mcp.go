package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// -------------------------------------------
// MCP 公开类型
// -------------------------------------------

// MCPManifest 表示工作目录下 mcp.json 的解析结果。
type MCPManifest struct {
	Servers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig 单个 MCP 服务器的连接配置。
//
// HTTP 传输：仅需 URL 字段。
// Stdio 传输：需 Command + 可选 Args / Env，URL 留空。
type MCPServerConfig struct {
	URL     string            `json:"url,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPTool 从 MCP 服务器 tools/list 返回的工具/技能定义。
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPServerStatus 返回 MCP 服务器及其工具的状态摘要。
type MCPServerStatus struct {
	Name      string           `json:"name"`
	Transport string           `json:"transport"` // "http" | "stdio"
	URL       string           `json:"url"`
	Command   string           `json:"command"`
	Enabled   bool             `json:"enabled"`
	Tools     int              `json:"tools"`
	Skills    []MCPSkillStatus `json:"skills"`
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
	Transport string            // "http" | "stdio"
	URL       string            // HTTP 传输
	Command   string            // Stdio 传输
	Args      []string          // Stdio 传输
	Env       map[string]string // Stdio 传输
	Enabled   bool
	Tools     []mcpToolEntry
	cmd       *exec.Cmd       // Stdio: 运行中的子进程
	stdin     io.WriteCloser  // Stdio: 子进程 stdin
	stdout    io.ReadCloser   // Stdio: 子进程 stdout
	stdioMu   sync.Mutex      // Stdio: 保护 stdin/stdout 并发读写
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
		info := &mcpServerInfo{
			URL:     cfg.URL,
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
		}
		if cfg.Command != "" {
			info.Transport = "stdio"
		} else {
			info.Transport = "http"
		}
		a.mcpServers[name] = info
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
			Name:      name,
			Transport: info.Transport,
			URL:       info.URL,
			Command:   info.Command,
			Enabled:   info.Enabled,
			Tools:     len(info.Tools),
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
// HTTP 传输：发送 initialize → tools/list 握手。
// Stdio 传输：启动子进程，通过 stdin/stdout 完成握手。
func (a *Agent) EnableMCP(mcpName string) error {
	a.mcpMu.RLock()
	info, ok := a.mcpServers[mcpName]
	a.mcpMu.RUnlock()
	if !ok {
		return fmt.Errorf("MCP %q 未在 mcp.json 中定义", mcpName)
	}

	var tools []MCPTool
	var err error
	if info.Transport == "stdio" {
		tools, err = a.listMCPToolsStdio(info)
	} else {
		tools, err = a.listMCPToolsHTTP(info.URL)
	}
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

// DisableMCP 禁用指定 MCP。
// 仅标记禁用，不删除工具数据。Stdio 传输会同时终止子进程。
func (a *Agent) DisableMCP(mcpName string) error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	info, ok := a.mcpServers[mcpName]
	if !ok {
		return fmt.Errorf("MCP %q 不存在", mcpName)
	}
	info.Enabled = false
	if info.cmd != nil && info.cmd.Process != nil {
		info.cmd.Process.Kill()
		info.cmd = nil
	}
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

// listMCPToolsHTTP 通过 MCP 初始化握手 + tools/list 获取工具列表（HTTP/SSE 传输）。
func (a *Agent) listMCPToolsHTTP(serverURL string) ([]MCPTool, error) {
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

// findMCPTool 在已启用的 MCP 服务器中查找指定工具，返回服务器信息。
func (a *Agent) findMCPTool(toolName string) (*mcpServerInfo, bool) {
	a.mcpMu.RLock()
	defer a.mcpMu.RUnlock()
	for _, info := range a.mcpServers {
		if !info.Enabled {
			continue
		}
		for _, t := range info.Tools {
			if t.Name == toolName && !t.Disabled {
				return info, true
			}
		}
	}
	return nil, false
}

// mcpToolCall 调用 MCP 服务器的 tools/call 并返回文本结果。
// 根据 info.Transport 自动选择 HTTP 或 Stdio 通道。
func (a *Agent) mcpToolCall(ctx context.Context, info *mcpServerInfo, toolName string, args map[string]any) (string, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	var result json.RawMessage
	var err error
	if info.Transport == "stdio" {
		result, err = a.mcpCallStdio(info, "tools/call", params)
	} else {
		result, err = a.mcpCall(info.URL, "tools/call", params)
	}
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

// -------------------------------------------
// MCP 协议客户端（JSON-RPC over Stdio）
// -------------------------------------------

// stdioRequestID 用于 stdio 传输中生成唯一的 JSON-RPC 请求 ID。
var stdioRequestID int32

// listMCPToolsStdio 通过启动子进程并完成 MCP 握手获取工具列表。
func (a *Agent) listMCPToolsStdio(info *mcpServerInfo) ([]MCPTool, error) {
	// 启动子进程
	cmd := exec.Command(info.Command, info.Args...)
	cmd.Env = os.Environ()
	for k, v := range info.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动子进程失败: %w", err)
	}
	info.cmd = cmd
	info.stdin = stdin
	info.stdout = stdout

	// 初始化握手
	_, err = a.mcpCallStdioRaw(info, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "SunnyNetAgentCore",
			"version": "1.0.0",
		},
	})
	if err != nil {
		cmd.Process.Kill()
		info.cmd = nil
		return nil, fmt.Errorf("initialize 失败: %w", err)
	}

	// 发送 initialized 通知
	_ = a.mcpNotifyStdioRaw(info, "notifications/initialized", nil)

	// 获取工具列表
	result, err := a.mcpCallStdioRaw(info, "tools/list", map[string]any{})
	if err != nil {
		cmd.Process.Kill()
		info.cmd = nil
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

// mcpCallStdio 通过 stdio 发送 JSON-RPC 请求并返回 result。
func (a *Agent) mcpCallStdio(info *mcpServerInfo, method string, params any) (json.RawMessage, error) {
	info.stdioMu.Lock()
	defer info.stdioMu.Unlock()
	return a.mcpCallStdioRaw(info, method, params)
}

// mcpCallStdioRaw 通过 info.stdin/stdout 发送请求并返回 result（调用方需持锁）。
func (a *Agent) mcpCallStdioRaw(info *mcpServerInfo, method string, params any) (json.RawMessage, error) {
	id := int(atomic.AddInt32(&stdioRequestID, 1))
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      &id,
	})
	if err != nil {
		return nil, err
	}
	// 新行分隔的 JSON
	line := append(body, '\n')
	if _, err := info.stdin.Write(line); err != nil {
		return nil, fmt.Errorf("stdio 写入失败: %w", err)
	}

	// 读取响应（一行 JSON）
	reader := bufio.NewReader(info.stdout)
	respLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("stdio 读取失败: %w", err)
	}

	var jr jsonrpcResponse
	if err := json.Unmarshal([]byte(respLine), &jr); err != nil {
		return nil, fmt.Errorf("JSON-RPC 解析失败: %w", err)
	}
	if jr.Error != nil {
		return nil, fmt.Errorf("JSON-RPC 错误 (code=%d): %s", jr.Error.Code, jr.Error.Message)
	}
	return jr.Result, nil
}

// mcpNotifyStdio 通过 stdio 发送 JSON-RPC 通知（无 id）。
func (a *Agent) mcpNotifyStdio(info *mcpServerInfo, method string, params any) error {
	info.stdioMu.Lock()
	defer info.stdioMu.Unlock()
	return a.mcpNotifyStdioRaw(info, method, params)
}

// mcpNotifyStdioRaw 通过 info.stdin 发送通知（调用方需持锁）。
func (a *Agent) mcpNotifyStdioRaw(info *mcpServerInfo, method string, params any) error {
	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	line := append(body, '\n')
	_, err = info.stdin.Write(line)
	return err
}
