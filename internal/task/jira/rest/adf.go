package rest

import (
	"encoding/json"
	"regexp"
	"strings"
)

var orderedItem = regexp.MustCompile(`^\d+\.\s+`)

func ADF(text string) map[string]any {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	content := make([]any, 0)
	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		if strings.HasPrefix(line, "```") {
			i++
			code := make([]string, 0)
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++
			}
			content = append(content, map[string]any{"type": "codeBlock", "content": []any{textNode(strings.Join(code, "\n"))}})
			continue
		}
		if strings.HasPrefix(line, "- ") || orderedItem.MatchString(line) {
			ordered := orderedItem.MatchString(line)
			items := make([]any, 0)
			for i < len(lines) {
				item := strings.TrimSpace(lines[i])
				if ordered {
					if !orderedItem.MatchString(item) {
						break
					}
					item = orderedItem.ReplaceAllString(item, "")
				} else {
					if !strings.HasPrefix(item, "- ") {
						break
					}
					item = strings.TrimPrefix(item, "- ")
				}
				items = append(items, map[string]any{
					"type":    "listItem",
					"content": []any{paragraph(item)},
				})
				i++
			}
			kind := "bulletList"
			if ordered {
				kind = "orderedList"
			}
			content = append(content, map[string]any{"type": kind, "content": items})
			continue
		}
		if heading(line) {
			content = append(content, map[string]any{
				"type":    "heading",
				"attrs":   map[string]any{"level": 3},
				"content": []any{textNode(strings.TrimSuffix(line, ":"))},
			})
			i++
			continue
		}
		content = append(content, paragraph(line))
		i++
	}
	if len(content) == 0 {
		content = append(content, paragraph(""))
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func heading(line string) bool {
	if len(line) > 80 {
		return false
	}
	return strings.HasSuffix(line, ":") || (line == strings.ToUpper(line) && strings.IndexFunc(line, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0)
}

func paragraph(text string) map[string]any {
	if text == "" {
		return map[string]any{"type": "paragraph"}
	}
	return map[string]any{"type": "paragraph", "content": inlineContent(text)}
}

func inlineContent(text string) []any {
	if strings.HasPrefix(text, "<!--") {
		return []any{textNode(text)}
	}
	if i := strings.Index(text, ":"); i > 0 && i < 40 {
		label := textNode(text[:i+1])
		label["marks"] = []any{map[string]any{"type": "strong"}}
		return []any{label, textNode(text[i+1:])}
	}
	return []any{textNode(text)}
}

func textNode(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func ADFText(raw json.RawMessage) string {
	var node struct {
		Text    string            `json:"text"`
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	parts := make([]string, 0, len(node.Content)+1)
	if node.Text != "" {
		parts = append(parts, node.Text)
	}
	for _, child := range node.Content {
		if text := ADFText(child); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
