package main

// A tiny YAML subset good enough to read and write the Python version's
// ~/.config/stt/config.yml (written by PyYAML's yaml.dump): block mappings,
// nested block mappings (backends), and block scalar lists (order). Keeping
// this in-tree preserves the zero-third-party-modules property.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ymlMap is an insertion-ordered string map whose values are string,
// *ymlMap, or []string.
type ymlMap struct {
	keys []string
	vals map[string]any
}

func newYmlMap() *ymlMap {
	return &ymlMap{vals: map[string]any{}}
}

func (m *ymlMap) Get(key string) (string, bool) {
	v, ok := m.vals[key]
	if !ok {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case ymlRaw:
		return string(s), true
	}
	return "", false
}

func (m *ymlMap) GetMap(key string) (*ymlMap, bool) {
	v, ok := m.vals[key]
	if !ok {
		return nil, false
	}
	mm, ok := v.(*ymlMap)
	return mm, ok
}

func (m *ymlMap) GetList(key string) ([]string, bool) {
	v, ok := m.vals[key]
	if !ok {
		return nil, false
	}
	l, ok := v.([]string)
	return l, ok
}

func (m *ymlMap) Keys() []string { return m.keys }

func (m *ymlMap) put(key string, value any) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
}

func (m *ymlMap) Set(key, value string) { m.put(key, value) }

// ymlRaw is a scalar emitted verbatim (unquoted), used to round-trip YAML
// booleans that PyYAML writes bare.
type ymlRaw string

func (m *ymlMap) SetMap(key string, sub *ymlMap) {
	m.put(key, sub)
}
func (m *ymlMap) SetList(key string, list []string) { m.put(key, list) }

func (m *ymlMap) Remove(key string) {
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

func (m *ymlMap) ensureMap(key string) *ymlMap {
	if sub, ok := m.GetMap(key); ok {
		return sub
	}
	sub := newYmlMap()
	m.SetMap(key, sub)
	return sub
}

// parseYAMLFile reads a config.yml-style file. A missing file yields nil.
func parseYAMLFile(path string) (*ymlMap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseYAML(string(data))
}

type ymlLine struct {
	indent int
	text   string // trimmed content
}

func parseYAML(text string) (*ymlMap, error) {
	var lines []ymlLine
	for n, raw := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "---" {
			continue
		}
		if strings.HasPrefix(raw, "\t") {
			return nil, fmt.Errorf("yaml line %d: tabs not supported", n+1)
		}
		lines = append(lines, ymlLine{
			indent: len(raw) - len(strings.TrimLeft(raw, " ")),
			text:   trimmed,
		})
	}
	pos := 0
	m, err := parseYMLBlock(lines, &pos, 0)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// parseYMLBlock consumes lines at exactly `indent` (recursing for deeper
// blocks) and returns the mapping.
func parseYMLBlock(lines []ymlLine, pos *int, indent int) (*ymlMap, error) {
	m := newYmlMap()
	for *pos < len(lines) {
		line := lines[*pos]
		if line.indent < indent {
			break
		}
		if strings.HasPrefix(line.text, "- ") || line.text == "-" {
			break // a list belongs to whoever declared the key
		}
		if line.indent > indent {
			*pos++ // stray deeper line outside a block; ignore
			continue
		}
		key, rest, ok := splitYAMLKey(line.text)
		if !ok {
			*pos++ // not a mapping line; ignore
			continue
		}
		*pos++
		if rest != "" {
			m.Set(key, ymlUnquote(stripYAMLComment(rest)))
			continue
		}
		// "key:" — a nested block or a null value. PyYAML dumps block
		// lists at the same indent as their key, mappings one deeper.
		if *pos < len(lines) {
			child := lines[*pos]
			isItem := strings.HasPrefix(child.text, "- ") || child.text == "-"
			switch {
			case isItem && child.indent >= indent:
				m.SetList(key, parseYMLList(lines, pos, child.indent))
			case !isItem && child.indent > indent:
				sub, err := parseYMLBlock(lines, pos, child.indent)
				if err != nil {
					return nil, err
				}
				m.SetMap(key, sub)
			default:
				m.Set(key, "") // null value
			}
		} else {
			m.Set(key, "") // null value
		}
	}
	return m, nil
}

func parseYMLList(lines []ymlLine, pos *int, indent int) []string {
	var out []string
	for *pos < len(lines) && lines[*pos].indent == indent {
		t := lines[*pos].text
		if !strings.HasPrefix(t, "- ") && t != "-" {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(t, "-"))
		out = append(out, ymlUnquote(stripYAMLComment(item)))
		*pos++
	}
	return out
}

// splitYAMLKey splits "key: value" honoring quoted keys. Returns ok=false for
// lines that are not mappings.
func splitYAMLKey(s string) (key, rest string, ok bool) {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			if i+1 == len(s) || s[i+1] == ' ' {
				return ymlUnquote(s[:i]), strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// stripYAMLComment removes a trailing " # comment" from a plain scalar.
func stripYAMLComment(s string) string {
	if s == "" || s[0] == '"' || s[0] == '\'' {
		return s
	}
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func ymlUnquote(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			if unq, err := strconv.Unquote(s); err == nil {
				return unq
			}
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return s
}

// yamlScalarNeedsQuotes mirrors PyYAML's decision to quote when a plain
// scalar would be ambiguous.
func yamlScalarNeedsQuotes(s string) bool {
	if s == "" {
		return true
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	if strings.TrimSpace(s) != s || strings.ContainsAny(s, "\n\t") {
		return true
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") || strings.Contains(s, " #") {
		return true
	}
	switch s[0] {
	case '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
		return true
	}
	return false
}

func yamlEmitScalar(s string) string {
	if yamlScalarNeedsQuotes(s) {
		return strconv.Quote(s)
	}
	return s
}

// String renders the document in the Python version's canonical key order.
func (m *ymlMap) String() string {
	var b strings.Builder
	ymlEmitMap(&b, m, 0, true)
	return b.String()
}

var yamlCanonicalOrder = []string{
	"language", "hotkey", "audio_device",
	"prompt", "sound_enabled", "keep_recordings",
	"benchmark", "backends", "order",
}

func ymlOrderedKeys(m *ymlMap, canonical bool) []string {
	if !canonical {
		return m.keys
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range yamlCanonicalOrder {
		if _, ok := m.vals[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range m.keys {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func ymlEmitMap(b *strings.Builder, m *ymlMap, depth int, canonical bool) {
	pad := strings.Repeat("  ", depth)
	for _, k := range ymlOrderedKeys(m, canonical) {
		switch v := m.vals[k].(type) {
		case string:
			b.WriteString(pad + yamlEmitKey(k) + ": " + yamlEmitScalar(v) + "\n")
		case ymlRaw:
			b.WriteString(pad + yamlEmitKey(k) + ": " + string(v) + "\n")
		case []string:
			b.WriteString(pad + yamlEmitKey(k) + ":\n")
			for _, item := range v {
				b.WriteString(pad + "- " + yamlEmitScalar(item) + "\n")
			}
		case *ymlMap:
			if len(v.keys) == 0 {
				b.WriteString(pad + yamlEmitKey(k) + ": {}\n")
				continue
			}
			b.WriteString(pad + yamlEmitKey(k) + ":\n")
			ymlEmitMap(b, v, depth+1, false)
		}
	}
}

func yamlEmitKey(k string) string {
	if yamlScalarNeedsQuotes(k) {
		return strconv.Quote(k)
	}
	return k
}
