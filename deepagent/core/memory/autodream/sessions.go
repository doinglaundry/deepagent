package autodream

import (
	"os"
	"strings"
	"time"
)

func ListJSONLSessionCandidates(dir string) ([]SessionCandidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SessionCandidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		out = append(out, SessionCandidate{SessionID: sessionID, Mtime: info.ModTime()})
	}
	return out, nil
}

func FilterSessionsTouchedSince(candidates []SessionCandidate, since time.Time, currentSessionID string) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.SessionID == currentSessionID {
			continue
		}
		if since.IsZero() || candidate.Mtime.After(since) {
			out = append(out, candidate.SessionID)
		}
	}
	return out
}
