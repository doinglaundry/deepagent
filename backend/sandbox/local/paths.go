// Package local: paths.go owns path-mapping translation between virtual
// container paths (/mnt/user-data/workspace/...) and host paths (/Users/.../
// .eino-cli/users/<uid>/threads/<tid>/user-data/workspace/...).
//
// All seven path resolvers are top-level functions taking the mappings
// slice as data — `Sandbox` only holds the per-instance state
// (id, mappings, agent-written-paths set). AGENTS.md: "Behavior lives in
// plain top-level functions".
package local

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/sandbox"
)

// PathMapping: container_path is what the LLM sees, local_path is the host
// directory we bind under it. ReadOnly maps to write_file → EROFS.
type PathMapping struct {
	ContainerPath string
	LocalPath     string
	ReadOnly      bool
}

// resolved bundles the rewrite result + which mapping fired (nil for
// passthrough — caller can tell "this is a real host path the LLM gave me"
// from "this is a mapped path I just rewrote").
type resolved struct {
	Path    string
	Mapping *PathMapping
}

// findPathMapping picks the most specific mapping whose container_path is
// a path-prefix of `path`. Longest first so /mnt/user-data/workspace wins
// over /mnt/user-data. Returns mapping + the suffix relative to the
// container mount root, or (nil, "") on no match.
func findPathMapping(mappings []PathMapping, path string) (*PathMapping, string) {
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(strings.TrimRight(sorted[i].ContainerPath, "/")) >
			len(strings.TrimRight(sorted[j].ContainerPath, "/"))
	})

	for i := range sorted {
		m := &sorted[i]
		container := strings.TrimRight(m.ContainerPath, "/")
		if container == "" {
			// Root mapping: every absolute path matches.
			if strings.HasPrefix(path, "/") {
				return &mappings[indexOf(mappings, *m)], strings.TrimPrefix(path, "/")
			}
			continue
		}
		if path == container || strings.HasPrefix(path, container+"/") {
			rel := strings.TrimPrefix(path[len(container):], "/")
			return &mappings[indexOf(mappings, *m)], rel
		}
	}
	return nil, ""
}

// indexOf finds m in slice by value (container_path is unique by
// construction in setupPathMappings). Returns the index into the original
// slice so callers get a stable *PathMapping pointer.
func indexOf(mappings []PathMapping, m PathMapping) int {
	for i, x := range mappings {
		if x.ContainerPath == m.ContainerPath && x.LocalPath == m.LocalPath {
			return i
		}
	}
	return 0
}

// resolvePath: container path → host path. Path-escape via `..` after the
// container prefix is rejected (returns PermissionError). Unmapped paths
// pass through unchanged so the LLM can still touch real host files when
// explicitly allowed.
func resolvePath(mappings []PathMapping, path string) (resolved, error) {
	m, rel := findPathMapping(mappings, path)
	if m == nil {
		return resolved{Path: path}, nil
	}
	localRoot, err := filepath.Abs(m.LocalPath)
	if err != nil {
		return resolved{}, err
	}
	joined := localRoot
	if rel != "" {
		joined = filepath.Join(localRoot, rel)
	}
	// Clean + Abs collapses `..`, so we re-test that the result is still
	// rooted under localRoot. This is the path-escape guard the LLM can't
	// trick by sending `/mnt/user-data/workspace/../../etc/passwd`.
	cleaned, err := filepath.Abs(joined)
	if err != nil {
		return resolved{}, err
	}
	if !isUnder(cleaned, localRoot) {
		return resolved{}, sandbox.NewPermissionError("path escapes mount root", path)
	}
	return resolved{Path: cleaned, Mapping: m}, nil
}

// isUnder: child == parent OR child == parent/..., using OS separator.
func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}

// reverseResolvePath: host path → container path (the inverse of
// resolvePath). Picks the longest matching local_path so a nested mount
// gets the more specific container prefix.
func reverseResolvePath(mappings []PathMapping, path string) string {
	cleaned, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		cleaned = path
	}

	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		ai, _ := filepath.Abs(sorted[i].LocalPath)
		aj, _ := filepath.Abs(sorted[j].LocalPath)
		return len(ai) > len(aj)
	})

	for _, m := range sorted {
		local, err := filepath.Abs(m.LocalPath)
		if err != nil {
			continue
		}
		if cleaned == local {
			return m.ContainerPath
		}
		if strings.HasPrefix(cleaned, local+string(filepath.Separator)) {
			rel := strings.TrimPrefix(cleaned[len(local):], string(filepath.Separator))
			rel = filepath.ToSlash(rel)
			if rel == "" {
				return m.ContainerPath
			}
			return strings.TrimRight(m.ContainerPath, "/") + "/" + rel
		}
	}
	return cleaned
}

// reverseResolvePathsInOutput: scan output for any host path that begins
// with a known local_path prefix and rewrite it to its container path.
// Used to mask host-file leakage in shell stdout / file content.
func reverseResolvePathsInOutput(mappings []PathMapping, output string) string {
	if len(mappings) == 0 || output == "" {
		return output
	}
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].LocalPath) > len(sorted[j].LocalPath)
	})
	result := output
	for _, m := range sorted {
		local, err := filepath.Abs(m.LocalPath)
		if err != nil {
			continue
		}
		// Match local prefix optionally followed by /sub/path/... up to a
		// shell-ish boundary. Same boundary class as deer-flow.
		pattern := regexp.QuoteMeta(local) + `(?:[/\\][^\s"';&|<>()]*)?`
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		result = re.ReplaceAllStringFunc(result, func(match string) string {
			return reverseResolvePath(mappings, match)
		})
	}
	return result
}

// resolvePathsInCommand: scan a shell command, rewrite every container
// path it mentions to the corresponding host path. Boundary class blocks
// `/mnt/skills` from accidentally matching inside `/mnt/skills-extra`.
func resolvePathsInCommand(mappings []PathMapping, command string) string {
	return rewriteContainerPaths(mappings, command, `(?:/|$|[\s"';&|<>()])`, false)
}

// resolvePathsInContent: same as Command, but for write_file content
// (plain text, not shell). Boundary class is laxer because content has no
// shell metachars. Output is normalised to forward slashes so we don't
// emit `C:\Users\...` into a Python source literal.
func resolvePathsInContent(mappings []PathMapping, content string) string {
	return rewriteContainerPaths(mappings, content, `(?:/|$|[^\w./-])`, true)
}

func rewriteContainerPaths(mappings []PathMapping, src, boundary string, forwardSlash bool) string {
	if len(mappings) == 0 || src == "" {
		return src
	}
	sorted := make([]PathMapping, len(mappings))
	copy(sorted, mappings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].ContainerPath) > len(sorted[j].ContainerPath)
	})

	var alts []string
	for _, m := range sorted {
		alts = append(alts, "("+regexp.QuoteMeta(m.ContainerPath)+boundary+`(?:/[^\s"';&|<>()]*)?`+")")
	}
	if len(alts) == 0 {
		return src
	}
	re, err := regexp.Compile(strings.Join(alts, "|"))
	if err != nil {
		return src
	}
	return re.ReplaceAllStringFunc(src, func(match string) string {
		// Strip boundary char that lookahead-style regex doesn't support in
		// RE2: we captured it, so put it back after rewriting the prefix.
		boundaryChar := ""
		core := match
		// Find which mapping matched (longest first ensures correctness).
		for _, m := range sorted {
			if strings.HasPrefix(core, m.ContainerPath) {
				rest := core[len(m.ContainerPath):]
				if rest == "" {
					return m.LocalPath
				}
				if rest[0] == '/' {
					r, err := resolvePath(mappings, core)
					if err != nil {
						return match
					}
					out := r.Path
					if forwardSlash {
						out = filepath.ToSlash(out)
					}
					return out
				}
				// boundary char (space, quote, etc.) follows directly.
				boundaryChar = string(rest[0])
				_ = boundaryChar
				return m.LocalPath + rest
			}
		}
		return match
	})
}

// isReadOnlyPath: walk mappings, pick the longest local_path that contains
// the resolved path, and return its ReadOnly flag.
func isReadOnlyPath(mappings []PathMapping, resolvedPath string) bool {
	cleaned, err := filepath.Abs(resolvedPath)
	if err != nil {
		cleaned = resolvedPath
	}
	var best *PathMapping
	bestLen := -1
	for i := range mappings {
		local, err := filepath.Abs(mappings[i].LocalPath)
		if err != nil {
			continue
		}
		if cleaned == local || strings.HasPrefix(cleaned, local+string(filepath.Separator)) {
			if len(local) > bestLen {
				bestLen = len(local)
				best = &mappings[i]
			}
		}
	}
	if best == nil {
		return false
	}
	return best.ReadOnly
}
