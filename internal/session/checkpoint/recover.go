package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func RecoverLatest(dir string) (Snapshot, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, fmt.Errorf("read checkpoints directory: %w", err)
	}

	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		files = append(files, entry)
	}

	if len(files) == 0 {
		return Snapshot{}, false, nil
	}

	sort.Slice(files, func(i, j int) bool {
		infoI, errI := files[i].Info()
		infoJ, errJ := files[j].Info()
		if errI != nil || errJ != nil {
			return files[i].Name() > files[j].Name()
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	payload, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("read checkpoint: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Snapshot{}, false, fmt.Errorf("unmarshal checkpoint: %w", err)
	}

	return snapshot, true, nil
}
