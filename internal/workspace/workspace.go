package workspace

import "eino-cli/internal/workspace/scan"

type Manifest = scan.Result

func Discover(root string) (Manifest, error) {
	return scan.Detect(root)
}
