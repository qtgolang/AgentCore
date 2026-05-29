package main

import "context"

// Skill 定义一个可供 LLM 调用的技能（函数）。
//
// 每个 Skill 对应一个 OpenAI function calling 中的 tool 定义：
//   - Name: 函数名，LLM 通过此名称发起调用
//   - Description: 函数描述，LLM 据此判断何时调用
//   - Parameters: JSON Schema 格式的参数定义，如 {"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}
//   - Execute: 实际执行函数，接收 LLM 传入的 JSON 参数 map，返回执行结果文本
//
// 使用方式：
//
//	agent.RegisterSkill(Skill{
//	    Name:        "md5",
//	    Description: "获取文件的 MD5 哈希值",
//	    Parameters: map[string]any{
//	        "type": "object",
//	        "properties": map[string]any{
//	            "file": map[string]any{"type": "string", "description": "文件路径"},
//	        },
//	        "required": []string{"file"},
//	    },
//	    Execute: func(ctx context.Context, args map[string]any) (string, error) {
//	        file, _ := args["file"].(string)
//	        return computeMD5(file)
//	    },
//	})
type Skill struct {
	Name        string                                                         // 函数名
	Description string                                                         // 函数描述
	Parameters  map[string]any                                                 // JSON Schema 参数定义
	Returns     string                                                         // 返回值描述，执行后会自动追加到结果中供 LLM 理解
	Execute     func(ctx context.Context, args map[string]any) (string, error) // 执行函数，args 为 LLM 传入的已解析参数
}

// buildToolDef 将 Skill 转换为 OpenAI tools 数组中的单个元素。
func (s Skill) buildToolDef() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        s.Name,
			"description": s.Description,
			"parameters":  s.Parameters,
		},
	}
}
