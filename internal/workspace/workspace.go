package workspace

import (
	"fmt"

	"eino-cli/internal/workspace/scan"
)

type Manifest = scan.Result

func Discover(root string) (Manifest, error) {
	manifest, err := scan.Detect(root)
	if err != nil {
		return Manifest{}, err
	}
	if !manifest.IsGitRepo {
		return Manifest{}, fmt.Errorf("workspace is not a git repository: %s", manifest.RootPath)
	}
	return manifest, nil
}
