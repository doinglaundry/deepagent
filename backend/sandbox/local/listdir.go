package local

import (
	"os"
	"path/filepath"
	"sort"

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
		if search.ShouldIgnoreName(e.Name()) {
			continue
		}
		full := filepath.Join(current, e.Name())
		listed, isDir, descend, ok := listEntry(root, full)
		if !ok {
			continue
		}
		*out = append(*out, listed)
		if descend && isDir && depth < maxDepth {
			if err := traverse(root, full, depth+1, maxDepth, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func listEntry(root, full string) (listed string, isDir, descend, ok bool) {
	info, err := os.Lstat(full)
	if err != nil {
		return "", false, false, false
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return full + dirSuffix(info), info.IsDir(), true, true
	}

	target, err := filepath.EvalSymlinks(full)
	if err != nil || !isUnder(target, root) {
		return "", false, false, false
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return "", false, false, false
	}
	return target + dirSuffix(targetInfo), targetInfo.IsDir(), false, true
}

func dirSuffix(info os.FileInfo) string {
	if info.IsDir() {
		return "/"
	}
	return ""
}
