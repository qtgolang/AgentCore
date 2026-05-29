package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// -------------------------------------------
// 内部类型
// -------------------------------------------

// sessionContext 是上下文文件的顶层 JSON 结构。
type sessionContext struct {
	Messages []chatMsg `json:"messages"`
}

// contextStore 管理对话上下文及相关文件的持久化。
// 所有路径为完整路径，由外部传入。
//
// 职责：
//   - 读写指定的会话上下文 JSON 文件（对话历史）
//   - 读写 memory.md / document.md / role.md
//   - 线程安全（内部 sync.Mutex）
type contextStore struct {
	contextPath  string     // 会话上下文文件完整路径
	memoryPath   string     // 长期记忆文件
	documentPath string     // 知识库文件
	rolePath     string     // 角色设定文件
	mu           sync.Mutex // 保护所有读写操作
}

// -------------------------------------------
// 生命周期
// -------------------------------------------

// newContextStore 创建上下文存储实例。
// 初始化时自动创建各文件（如不存在则写入默认模板），并确保父目录存在。
func newContextStore(w Work) *contextStore {
	w.UserFile = strings.TrimSpace(w.UserFile)
	w.Role = strings.TrimSpace(w.Role)
	w.Memory = strings.TrimSpace(w.Memory)
	w.Document = strings.TrimSpace(w.Document)
	ensureFile(w.UserFile, []byte("{\n  \"messages\": []\n}\n"))
	ensureFile(w.Memory, []byte("# 长期记忆：在这里记录用户偏好、历史决策、个人信息等。\n"))
	ensureFile(w.Document, []byte("# 知识库：在这里填写项目文档、API 说明、业务规则等。\n"))
	ensureFile(w.Role, []byte("# 角色设定：在这里定义 AI 助手的身份、语气、行为准则等。\n"))
	return &contextStore{
		contextPath:  w.UserFile,
		memoryPath:   w.Memory,
		documentPath: w.Document,
		rolePath:     w.Role,
	}
}

// RoleDir 返回角色文件所在目录（用于定位 skills/ 等相对资源）。
func (s *contextStore) RoleDir() string {
	return filepath.Dir(s.rolePath)
}

// -------------------------------------------
// 路径（公开）
// -------------------------------------------

func (s *contextStore) MemoryPath() string   { return s.memoryPath }
func (s *contextStore) DocumentPath() string { return s.documentPath }
func (s *contextStore) RolePath() string     { return s.rolePath }

// -------------------------------------------
// 文件初始化
// -------------------------------------------

// ensureFile 在指定路径不存在时创建并写入默认内容。
func ensureFile(path string, defaultContent []byte) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(path, defaultContent, 0o644)
}

// -------------------------------------------
// 对话历史读写
// -------------------------------------------

// load 读取完整对话历史。文件不存在或解析失败时返回 nil。
func (s *contextStore) load() []chatMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.contextPath)
	if err != nil {
		return nil
	}
	var sc sessionContext
	if json.Unmarshal(data, &sc) != nil {
		return nil
	}
	return sc.Messages
}

// replace 用新的消息列表完全替换对话历史。
func (s *contextStore) replace(msgs []chatMsg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := sessionContext{Messages: msgs}
	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.contextPath, raw, 0o644)
}

// append 向对话历史末尾追加消息。
func (s *contextStore) append(msgs ...chatMsg) error {
	if len(msgs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var sc sessionContext
	if data, err := os.ReadFile(s.contextPath); err == nil {
		_ = json.Unmarshal(data, &sc)
	}
	sc.Messages = append(sc.Messages, msgs...)
	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.contextPath, raw, 0o644)
}

// clear 删除上下文文件。
func (s *contextStore) clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.contextPath)
}

// -------------------------------------------
// 文件读取
// -------------------------------------------

// loadMemory 读取 long-term memory 文件。
func (s *contextStore) loadMemory() string {
	data, err := os.ReadFile(s.memoryPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadDocument 读取知识库文件。
func (s *contextStore) loadDocument() string {
	data, err := os.ReadFile(s.documentPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadRole 读取角色设定文件。
func (s *contextStore) loadRole() string {
	data, err := os.ReadFile(s.rolePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// -------------------------------------------
// 工具函数
// -------------------------------------------

// buildSystemContent 将角色设定、长期记忆、知识库、技能文档拼接为 system message。
func buildSystemContent(role, memory, document, skillDocs string) string {
	role = strings.TrimSpace(role)
	memory = strings.TrimSpace(memory)
	document = strings.TrimSpace(document)
	skillDocs = strings.TrimSpace(skillDocs)

	var parts []string
	if role != "" {
		parts = append(parts, "【角色设定】\n"+role)
	}
	if memory != "" {
		parts = append(parts, "【长期记忆】\n"+memory)
	}
	if document != "" {
		parts = append(parts, "【知识库】\n"+document)
	}
	if skillDocs != "" {
		parts = append(parts, "【技能文档】\n"+skillDocs)
	}
	return strings.Join(parts, "\n\n")
}
