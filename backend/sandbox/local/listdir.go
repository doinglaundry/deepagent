package local

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"eino-cli/backend/sandbox/search"
)

// listDir returns depth-limited absolute paths under path; dirs get a trailing "/".
func listDir(path string, maxDepth int) ([]string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, nil
	}

	var out []string
	if err := traverse(root, root, 1, maxDepth, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func traverse(root, current string, depth, maxDepth int, out *[]string) error {
	if depth > maxDepth {
		return nil
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if search.ShouldIgnoreName(name) {
			continue
		}
		full := filepath.Join(current, name)
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		// Symlinks: only keep targets under root to avoid leaking /tmp via a workspace link.
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(full)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(target, root+string(filepath.Separator)) && target != root {
				continue
			}
			targetInfo, err := os.Stat(target)
			if err != nil {
				continue
			}
			suffix := ""
			if targetInfo.IsDir() {
				suffix = "/"
			}
			*out = append(*out, target+suffix)
			continue
		}
		suffix := ""
		if info.IsDir() {
			suffix = "/"
		}
		*out = append(*out, full+suffix)
		if info.IsDir() && depth < maxDepth {
			if err := traverse(root, full, depth+1, maxDepth, out); err != nil {
				return err
			}
		}
	}
	return nil
}
