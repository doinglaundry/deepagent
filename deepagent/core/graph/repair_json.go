package graph

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

// repairToolArguments strips an enclosing Markdown code fence and trailing commas only.
// It never invents missing field names, quotes, values, or delimiters.
func repairToolArguments(input string) (string, error) {
	s := strings.TrimSpace(input)
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		i := strings.IndexByte(s, '\n')
		if i >= 0 {
			s = strings.TrimSpace(s[i+1 : len(s)-3])
		}
	}
	var b strings.Builder
	quoted, escaped := false, false
	for i, r := range s {
		if quoted {
			b.WriteRune(r)
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				quoted = false
			}
			continue
		}
		if r == '"' {
			quoted = true
			b.WriteRune(r)
			continue
		}
		if r == ',' {
			j := i + 1
			for j < len(s) && unicode.IsSpace(rune(s[j])) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteRune(r)
	}
	out := b.String()
	if !json.Valid([]byte(out)) {
		return "", errors.New("invalid JSON arguments; only code fences and trailing commas can be repaired")
	}
	return out, nil
}
