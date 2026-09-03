package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rajpopat27/relay-flow/internal/config"
)

const relayFlowPlugin = "relay-flow-plugin@0.2.2-alpha"

type jsoncToken struct {
	kind       byte
	start, end int
	value      string
}

type jsoncValue struct {
	kind       byte
	startToken int
	endToken   int
	members    []jsoncMember
	elements   []jsoncValue
	closeToken int
}

type jsoncMember struct {
	name  string
	value jsoncValue
}

type jsoncParser struct {
	tokens []jsoncToken
	index  int
}

type textEdit struct {
	start, end int
	text       string
}

func setupRepo(repoPath string) error {
	path, mode, err := openCodeConfigPath(repoPath)
	if err != nil {
		return err
	}
	if err := ensurePluginConfig(path, mode, ""); err != nil {
		return err
	}

	tuiPath, tuiMode, err := openCodeTUIConfigPath(repoPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tuiPath), 0o755); err != nil {
		return fmt.Errorf("opencode: create TUI config directory: %w", err)
	}
	if err := ensurePluginConfig(tuiPath, tuiMode, "https://opencode.ai/tui.json"); err != nil {
		return fmt.Errorf("opencode: setup TUI config: %w", err)
	}
	return nil
}

func ensurePluginConfig(path string, mode os.FileMode, schema string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("opencode: read %s: %w", path, err)
		}
		if schema == "" {
			data = []byte("{\n  \"plugin\": [\"" + relayFlowPlugin + "\"]\n}\n")
		} else {
			data = []byte("{\n  \"$schema\": \"" + schema + "\",\n  \"plugin\": [\"" + relayFlowPlugin + "\"]\n}\n")
		}
	} else {
		data, err = updateOpenCodeConfig(data)
		if err != nil {
			return fmt.Errorf("opencode: update %s: %w", path, err)
		}
	}
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == string(data) {
		return nil
	}
	if err := config.WriteAtomic(path, data, mode); err != nil {
		return fmt.Errorf("opencode: write config: %w", err)
	}
	return nil
}

func openCodeConfigPath(repoPath string) (string, os.FileMode, error) {
	jsonPath := filepath.Join(repoPath, "opencode.json")
	jsoncPath := filepath.Join(repoPath, "opencode.jsonc")
	for _, path := range []string{jsonPath, jsoncPath} {
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", 0, fmt.Errorf("opencode: config %s is not a regular file", path)
			}
			return path, info.Mode().Perm(), nil
		}
		if !os.IsNotExist(err) {
			return "", 0, fmt.Errorf("opencode: inspect %s: %w", path, err)
		}
	}
	return jsonPath, 0o644, nil
}

func openCodeTUIConfigPath(repoPath string) (string, os.FileMode, error) {
	dir := filepath.Join(repoPath, ".opencode")
	for _, name := range []string{"tui.json", "tui.jsonc"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", 0, fmt.Errorf("opencode: TUI config %s is not a regular file", path)
			}
			return path, info.Mode().Perm(), nil
		}
		if !os.IsNotExist(err) {
			return "", 0, fmt.Errorf("opencode: inspect TUI config %s: %w", path, err)
		}
	}
	return filepath.Join(dir, "tui.json"), 0o644, nil
}

func updateOpenCodeConfig(data []byte) ([]byte, error) {
	tokens, err := tokenizeJSONC(data)
	if err != nil {
		return nil, err
	}
	p := jsoncParser{tokens: tokens}
	root, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if p.index != len(tokens) || root.kind != '{' {
		return nil, fmt.Errorf("config root must be one JSON object")
	}

	var plugins *jsoncValue
	for i := range root.members {
		if root.members[i].name == "plugin" {
			if plugins != nil {
				return nil, fmt.Errorf("duplicate plugin property")
			}
			plugins = &root.members[i].value
		}
	}
	quoted, _ := json.Marshal(relayFlowPlugin)
	if plugins == nil {
		return addPluginProperty(data, tokens, root, string(quoted)), nil
	}
	if plugins.kind != '[' {
		return nil, fmt.Errorf("plugin property must be an array")
	}

	first := -1
	duplicates := make([]int, 0)
	for i, element := range plugins.elements {
		tok := tokens[element.startToken]
		if element.kind != 's' {
			return nil, fmt.Errorf("plugin array element %d must be a string", i)
		}
		if tok.value == "relay-flow-plugin" || strings.HasPrefix(tok.value, "relay-flow-plugin@") {
			if first < 0 {
				first = i
			} else {
				duplicates = append(duplicates, i)
			}
		}
	}

	edits := make([]textEdit, 0, len(duplicates)+1)
	if first < 0 {
		edits = append(edits, addArrayElementEdit(tokens, *plugins, string(quoted)))
	} else {
		tok := tokens[plugins.elements[first].startToken]
		edits = append(edits, textEdit{start: tok.start, end: tok.end, text: string(quoted)})
		for _, index := range duplicates {
			element := plugins.elements[index]
			comma := commaBefore(tokens, plugins.elements[index-1].endToken, element.startToken)
			edits = append(edits,
				textEdit{start: tokens[comma].start, end: tokens[comma].end},
				textEdit{start: tokens[element.startToken].start, end: tokens[element.endToken].end},
			)
		}
	}
	return applyTextEdits(data, edits), nil
}

func addPluginProperty(data []byte, tokens []jsoncToken, root jsoncValue, quoted string) []byte {
	closeAt := tokens[root.closeToken].start
	edits := []textEdit{{
		start: closeAt,
		end:   closeAt,
		text:  "\n  \"plugin\": [" + quoted + "]\n",
	}}
	if len(root.members) > 0 {
		last := root.members[len(root.members)-1].value.endToken
		if commaBefore(tokens, last, root.closeToken) < 0 {
			at := tokens[last].end
			edits = append(edits, textEdit{start: at, end: at, text: ","})
		}
	}
	return applyTextEdits(data, edits)
}

func addArrayElementEdit(tokens []jsoncToken, array jsoncValue, quoted string) textEdit {
	if len(array.elements) == 0 {
		at := tokens[array.closeToken].start
		return textEdit{start: at, end: at, text: quoted}
	}
	last := array.elements[len(array.elements)-1]
	if comma := commaBefore(tokens, last.endToken, array.closeToken); comma >= 0 {
		at := tokens[comma].end
		return textEdit{start: at, end: at, text: " " + quoted}
	}
	at := tokens[last.endToken].end
	return textEdit{start: at, end: at, text: ", " + quoted}
}

func commaBefore(tokens []jsoncToken, after, before int) int {
	for i := after + 1; i < before; i++ {
		if tokens[i].kind == ',' {
			return i
		}
	}
	return -1
}

func applyTextEdits(data []byte, edits []textEdit) []byte {
	for i := 0; i < len(edits); i++ {
		for j := i + 1; j < len(edits); j++ {
			if edits[j].start > edits[i].start {
				edits[i], edits[j] = edits[j], edits[i]
			}
		}
	}
	out := append([]byte(nil), data...)
	for _, edit := range edits {
		out = append(out[:edit.start], append([]byte(edit.text), out[edit.end:]...)...)
	}
	return out
}

func tokenizeJSONC(data []byte) ([]jsoncToken, error) {
	tokens := make([]jsoncToken, 0)
	for i := 0; i < len(data); {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			i++
			continue
		case '/':
			if i+1 >= len(data) {
				return nil, fmt.Errorf("unexpected slash at byte %d", i)
			}
			if data[i+1] == '/' {
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			}
			if data[i+1] == '*' {
				start := i
				i += 2
				for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
					i++
				}
				if i+1 >= len(data) {
					return nil, fmt.Errorf("unterminated comment at byte %d", start)
				}
				i += 2
				continue
			}
			return nil, fmt.Errorf("unexpected slash at byte %d", i)
		case '{', '}', '[', ']', ':', ',':
			tokens = append(tokens, jsoncToken{kind: data[i], start: i, end: i + 1})
			i++
		case '"':
			start := i
			i++
			for i < len(data) {
				if data[i] == '\\' {
					i += 2
					continue
				}
				if data[i] == '"' {
					i++
					break
				}
				i++
			}
			if i > len(data) || data[i-1] != '"' {
				return nil, fmt.Errorf("unterminated string at byte %d", start)
			}
			var value string
			if err := json.Unmarshal(data[start:i], &value); err != nil {
				return nil, fmt.Errorf("invalid string at byte %d: %w", start, err)
			}
			tokens = append(tokens, jsoncToken{kind: 's', start: start, end: i, value: value})
		default:
			start := i
			for i < len(data) && !strings.ContainsRune(" \t\r\n{}[]:,/", rune(data[i])) {
				i++
			}
			if start == i || !json.Valid(data[start:i]) {
				return nil, fmt.Errorf("invalid value at byte %d", start)
			}
			tokens = append(tokens, jsoncToken{kind: 'v', start: start, end: i})
		}
	}
	return tokens, nil
}

func (p *jsoncParser) parseValue() (jsoncValue, error) {
	if p.index >= len(p.tokens) {
		return jsoncValue{}, fmt.Errorf("expected value")
	}
	start := p.index
	tok := p.tokens[p.index]
	switch tok.kind {
	case 's', 'v':
		p.index++
		return jsoncValue{kind: tok.kind, startToken: start, endToken: start}, nil
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	default:
		return jsoncValue{}, fmt.Errorf("unexpected token %q at byte %d", tok.kind, tok.start)
	}
}

func (p *jsoncParser) parseObject() (jsoncValue, error) {
	value := jsoncValue{kind: '{', startToken: p.index}
	p.index++
	for {
		if p.index >= len(p.tokens) {
			return jsoncValue{}, fmt.Errorf("unterminated object")
		}
		if p.tokens[p.index].kind == '}' {
			value.closeToken = p.index
			value.endToken = p.index
			p.index++
			return value, nil
		}
		key := p.tokens[p.index]
		if key.kind != 's' {
			return jsoncValue{}, fmt.Errorf("object key at byte %d must be a string", key.start)
		}
		p.index++
		if p.index >= len(p.tokens) || p.tokens[p.index].kind != ':' {
			return jsoncValue{}, fmt.Errorf("object key %q is missing a colon", key.value)
		}
		p.index++
		memberValue, err := p.parseValue()
		if err != nil {
			return jsoncValue{}, err
		}
		value.members = append(value.members, jsoncMember{name: key.value, value: memberValue})
		if p.index < len(p.tokens) && p.tokens[p.index].kind == ',' {
			p.index++
			continue
		}
		if p.index >= len(p.tokens) || p.tokens[p.index].kind != '}' {
			return jsoncValue{}, fmt.Errorf("object member %q is missing a comma", key.value)
		}
	}
}

func (p *jsoncParser) parseArray() (jsoncValue, error) {
	value := jsoncValue{kind: '[', startToken: p.index}
	p.index++
	for {
		if p.index >= len(p.tokens) {
			return jsoncValue{}, fmt.Errorf("unterminated array")
		}
		if p.tokens[p.index].kind == ']' {
			value.closeToken = p.index
			value.endToken = p.index
			p.index++
			return value, nil
		}
		element, err := p.parseValue()
		if err != nil {
			return jsoncValue{}, err
		}
		value.elements = append(value.elements, element)
		if p.index < len(p.tokens) && p.tokens[p.index].kind == ',' {
			p.index++
			continue
		}
		if p.index >= len(p.tokens) || p.tokens[p.index].kind != ']' {
			return jsoncValue{}, fmt.Errorf("array element is missing a comma")
		}
	}
}
