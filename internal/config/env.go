package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// expandString replaces ${NAME} with the looked-up value. "$$" yields a literal "$";
// a "$" not followed by "{" or "$" is kept as is.
func expandString(s string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("未結束的 ${：%q", s)
			}
			name := s[i+2 : i+2+end]
			v, ok := lookup(name)
			if !ok {
				return "", fmt.Errorf("環境變數 %s 未設定", name)
			}
			b.WriteString(v)
			i += 2 + end
		default:
			b.WriteByte('$')
		}
	}
	return b.String(), nil
}

// expandNode walks the YAML tree and expands ${NAME} inside every scalar value
// (map keys are left alone). Expansion happens after parsing, so secrets that
// contain YAML syntax characters cannot corrupt the document.
func expandNode(n *yaml.Node, path string, lookup func(string) (string, bool)) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := expandNode(c, path, lookup); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			p := key
			if path != "" {
				p = path + "." + key
			}
			if err := expandNode(n.Content[i+1], p, lookup); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := expandNode(c, fmt.Sprintf("%s[%d]", path, i), lookup); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		v, err := expandString(n.Value, lookup)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if v != n.Value {
			n.Value = v
			if n.Style == 0 {
				n.Tag = "" // let yaml re-resolve plain scalars (e.g. chat_id: ${CHAT})
			}
		}
	}
	return nil
}
