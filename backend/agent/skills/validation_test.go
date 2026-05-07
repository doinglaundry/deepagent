package skills

import (
	"strings"
	"testing"
)

func TestValidateFrontmatter_Pass(t *testing.T) {
	content := []byte(`---
name: my-skill
description: Use this when reviewing pull requests.
license: MIT
allowed-tools:
  - read
  - search
version: "1.0"
---

# Body
`)
	name, reason := ValidateFrontmatter(content)
	if reason != "" {
		t.Fatalf("expected pass, got reason: %s", reason)
	}
	if name != "my-skill" {
		t.Fatalf("name: got %q, want my-skill", name)
	}
}

func TestValidateFrontmatter_NoFrontmatter(t *testing.T) {
	_, reason := ValidateFrontmatter([]byte("just body, no fences."))
	if reason == "" {
		t.Fatal("expected non-empty reason for missing frontmatter")
	}
}

func TestValidateFrontmatter_UnknownKey(t *testing.T) {
	content := []byte(`---
name: my-skill
description: ok.
malicious: arbitrary value
---
`)
	_, reason := ValidateFrontmatter(content)
	if !strings.Contains(reason, "malicious") {
		t.Fatalf("expected unknown-key rejection mentioning 'malicious', got: %s", reason)
	}
}

func TestValidateFrontmatter_BadName(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // substring expected in reason
	}{
		{"uppercase", "BadName", "hyphen-case"},
		{"underscore", "bad_name", "hyphen-case"},
		{"leading hyphen", "-bad", "hyphen"},
		{"trailing hyphen", "bad-", "hyphen"},
		{"double hyphen", "a--b", "hyphen"},
		{"empty", "", "empty"},
		{"too long", strings.Repeat("a", 65), "too long"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := []byte("---\nname: " + c.raw + "\ndescription: x\n---\n")
			_, reason := ValidateFrontmatter(content)
			if !strings.Contains(strings.ToLower(reason), strings.ToLower(c.want)) {
				t.Fatalf("name %q: expected reason containing %q, got %q", c.raw, c.want, reason)
			}
		})
	}
}

func TestValidateFrontmatter_RejectAngleBrackets(t *testing.T) {
	content := []byte(`---
name: my-skill
description: This <thing> tries to break XML.
---
`)
	_, reason := ValidateFrontmatter(content)
	if !strings.Contains(reason, "angle bracket") {
		t.Fatalf("expected angle-bracket rejection, got: %s", reason)
	}
}

func TestValidateFrontmatter_TooLongDescription(t *testing.T) {
	content := []byte("---\nname: my-skill\ndescription: " + strings.Repeat("x", 1025) + "\n---\n")
	_, reason := ValidateFrontmatter(content)
	if !strings.Contains(reason, "too long") {
		t.Fatalf("expected description length rejection, got: %s", reason)
	}
}

func TestIsEnabled(t *testing.T) {
	cases := []struct {
		desc     string
		name     string
		category string
		enabled  map[string]bool
		want     bool
	}{
		{"unlisted public defaults true", "x", "public", nil, true},
		{"unlisted custom defaults true", "x", "custom", nil, true},
		{"unlisted unknown category defaults false", "x", "weird", nil, false},
		{"explicit false wins", "x", "public", map[string]bool{"x": false}, false},
		{"explicit true wins", "x", "weird", map[string]bool{"x": true}, true},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := IsEnabled(c.name, c.category, c.enabled); got != c.want {
				t.Fatalf("IsEnabled(%q, %q, %v) = %v, want %v", c.name, c.category, c.enabled, got, c.want)
			}
		})
	}
}
