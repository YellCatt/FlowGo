package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// templateRoots 允许省略前导点的顶层变量名，便于书写 {{ trigger.x }}。
var templateRoots = []string{"trigger", "nodes", "workflow", "run"}

// renderString 使用 text/template 渲染字符串，data 为模板变量上下文。
func renderString(tmpl string, data map[string]any) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("node").Option("missingkey=zero").Parse(normalizeTemplate(tmpl))
	if err != nil {
		return "", fmt.Errorf("invalid template %q: %w", tmpl, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render template %q: %w", tmpl, err)
	}
	return buf.String(), nil
}

// renderConfig 深度渲染节点配置：字符串做模板替换，映射与切片递归处理。
func renderConfig(cfg map[string]any, data map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		rendered, err := renderValue(v, data)
		if err != nil {
			return nil, fmt.Errorf("config field %q: %w", k, err)
		}
		out[k] = rendered
	}
	return out, nil
}

// renderValue 递归渲染任意配置值。
func renderValue(v any, data map[string]any) (any, error) {
	switch val := v.(type) {
	case string:
		return renderString(val, data)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			r, err := renderValue(item, data)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			r, err := renderValue(item, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

// normalizeTemplate 为省略前导点的顶层变量补上 "."，
// 使 {{ trigger.user }} 与标准写法 {{ .trigger.user }} 等价。
// 只处理 {{ }} 内部的动作片段，其余文本原样保留。
func normalizeTemplate(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		start := strings.Index(s[i:], "{{")
		if start < 0 {
			b.WriteString(s[i:])
			break
		}
		start += i
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		end += start + 2
		b.WriteString(s[i:start])
		b.WriteString("{{")
		b.WriteString(normalizeAction(s[start+2 : end]))
		b.WriteString("}}")
		i = end + 2
	}
	return b.String()
}

// normalizeAction 在单个模板动作内，为缺少前导点的顶层变量补点。
func normalizeAction(s string) string {
	var b strings.Builder
	tokenStart := true
	for i := 0; i < len(s); {
		c := s[i]
		if tokenStart && c != '.' && c != '$' && c != ' ' {
			if root, ok := matchRoot(s[i:]); ok {
				b.WriteByte('.')
				b.WriteString(root)
				i += len(root)
				tokenStart = false
				continue
			}
		}
		b.WriteByte(c)
		tokenStart = c == '|' || c == '(' || c == ',' || c == ' '
		i++
	}
	return b.String()
}

// matchRoot 判断 s 是否以顶层变量名开头且其后不构成更长的标识符。
func matchRoot(s string) (string, bool) {
	for _, root := range templateRoots {
		if !strings.HasPrefix(s, root) {
			continue
		}
		rest := s[len(root):]
		if rest == "" || !isIdentChar(rest[0]) {
			return root, true
		}
	}
	return "", false
}

// isIdentChar 判断字符是否可作为标识符的一部分。
func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// decodePayload 将触发负载 JSON 解析为模板变量可用的结构，失败时退化为原始字符串。
func decodePayload(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	return v
}
