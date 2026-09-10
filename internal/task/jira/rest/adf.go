package rest

import (
	"encoding/json"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var markdown = goldmark.New()

// ADF converts explicit Markdown syntax to Atlassian Document Format.
func ADF(value string) map[string]any {
	source := []byte(strings.ReplaceAll(value, "\r\n", "\n"))
	document := markdown.Parser().Parse(text.NewReader(source))
	content := blockContent(document, source)
	if len(content) == 0 {
		content = []any{paragraph(nil)}
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func blockContent(parent ast.Node, source []byte) []any {
	content := make([]any, 0, parent.ChildCount())
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		switch node := node.(type) {
		case *ast.Heading:
			content = append(content, map[string]any{
				"type":    "heading",
				"attrs":   map[string]any{"level": node.Level},
				"content": inlineContent(node, source, nil),
			})
		case *ast.Paragraph, *ast.TextBlock:
			content = append(content, paragraph(inlineContent(node, source, nil)))
		case *ast.List:
			content = append(content, list(node, source))
		case *ast.FencedCodeBlock:
			content = append(content, codeBlock(node.Text(source), node.Language(source)))
		case *ast.CodeBlock:
			content = append(content, codeBlock(node.Text(source), nil))
		case *ast.HTMLBlock:
			content = append(content, paragraph([]any{textNode(string(node.Text(source)), nil)}))
		default:
			content = append(content, blockContent(node, source)...)
		}
	}
	return content
}

func list(value *ast.List, source []byte) map[string]any {
	items := make([]any, 0, value.ChildCount())
	for child := value.FirstChild(); child != nil; child = child.NextSibling() {
		itemContent := blockContent(child, source)
		if len(itemContent) == 0 {
			itemContent = []any{paragraph(nil)}
		}
		items = append(items, map[string]any{"type": "listItem", "content": itemContent})
	}

	kind := "bulletList"
	result := map[string]any{"type": kind, "content": items}
	if value.IsOrdered() {
		result["type"] = "orderedList"
		if value.Start != 1 {
			result["attrs"] = map[string]any{"order": value.Start}
		}
	}
	return result
}

func codeBlock(value, language []byte) map[string]any {
	result := map[string]any{
		"type":    "codeBlock",
		"content": []any{textNode(strings.TrimSuffix(string(value), "\n"), nil)},
	}
	if len(language) != 0 {
		result["attrs"] = map[string]any{"language": string(language)}
	}
	return result
}

func paragraph(content []any) map[string]any {
	result := map[string]any{"type": "paragraph"}
	if len(content) != 0 {
		result["content"] = content
	}
	return result
}

func inlineContent(parent ast.Node, source []byte, marks []any) []any {
	content := make([]any, 0, parent.ChildCount())
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		switch node := node.(type) {
		case *ast.Text:
			if value := string(node.Text(source)); value != "" {
				content = append(content, textNode(value, marks))
			}
			if node.HardLineBreak() {
				content = append(content, map[string]any{"type": "hardBreak"})
			} else if node.SoftLineBreak() {
				content = append(content, textNode(" ", marks))
			}
		case *ast.String:
			if value := string(node.Value); value != "" {
				content = append(content, textNode(value, marks))
			}
		case *ast.Emphasis:
			kind := "em"
			if node.Level == 2 {
				kind = "strong"
			}
			content = append(content, inlineContent(node, source, withMark(marks, map[string]any{"type": kind}))...)
		case *ast.Link:
			mark := map[string]any{"type": "link", "attrs": map[string]any{"href": string(node.Destination)}}
			content = append(content, inlineContent(node, source, withMark(marks, mark))...)
		case *ast.AutoLink:
			content = append(content, textNode(string(node.Label(source)), withMark(marks, map[string]any{
				"type": "link", "attrs": map[string]any{"href": string(node.URL(source))},
			})))
		case *ast.CodeSpan:
			content = append(content, inlineContent(node, source, withMark(marks, map[string]any{"type": "code"}))...)
		case *ast.RawHTML:
			content = append(content, textNode(string(node.Text(source)), marks))
		default:
			content = append(content, inlineContent(node, source, marks)...)
		}
	}
	return content
}

func withMark(marks []any, mark map[string]any) []any {
	result := make([]any, len(marks), len(marks)+1)
	copy(result, marks)
	return append(result, mark)
}

func textNode(value string, marks []any) map[string]any {
	result := map[string]any{"type": "text", "text": value}
	if len(marks) != 0 {
		result["marks"] = marks
	}
	return result
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
