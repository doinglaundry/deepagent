package local

import (
	"os"
	"path/filepath"
	"sort"

	"eino-cli/deepagent/sandbox/search"
)

// listDir returns depth-limited absolute paths under path; dirs get a trailing "/".
func listDir(path string, maxDepth int) ([]string, error) {
	rootPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return listDirRoot(root, ".", maxDepth)
}

func listDirRoot(root *os.Root, relativePath string, maxDepth int) ([]string, error) {
	info, err := root.Stat(relativePath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var out []string
	err = traverse(root, relativePath, 1, maxDepth, &out)
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func traverse(root *os.Root, current string, depth, maxDepth int, out *[]string) error {
	if depth > maxDepth {
		return nil
	}
	directory, err := root.Open(current)
	if err != nil {
		return nil
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
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
			err = traverse(root, full, depth+1, maxDepth, out)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func listEntry(root *os.Root, full string) (listed string, isDir, descend, ok bool) {
	info, err := root.Lstat(full)
	if err != nil {
		return "", false, false, false
	}
	listed = filepath.Join(root.Name(), full)
	if info.Mode()&os.ModeSymlink != 0 {
		targetInfo, err := root.Stat(full)
		if err != nil {
			return "", false, false, false
		}
		return listed + dirSuffix(targetInfo), targetInfo.IsDir(), false, true
	}
	return listed + dirSuffix(info), info.IsDir(), true, true
}

func dirSuffix(info os.FileInfo) string {
	if info.IsDir() {
		return "/"
	}
	return ""
}
