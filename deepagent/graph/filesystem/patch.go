package filesystem

import (
	"context"
	"fmt"
	"strings"
)

type patchFile struct {
	operation string
	path      string
	moveTo    string
	lines     []string
}

// ApplyWorkspacePatch applies the file-oriented patch format to either workspace.
// Parse and validate every edit before performing the first write.
func ApplyWorkspacePatch(ctx context.Context, ws Filesystem, raw string) (string, error) {
	files, err := parseWorkspacePatch(raw)
	if err != nil {
		return "", err
	}
	patcher, ok := ws.(patchFilesystem)
	if !ok {
		return "", fmt.Errorf("filesystem does not support safe patch creation")
	}
	type change struct{ op, path, moveTo, content string }
	changes := make([]change, 0, len(files))
	resolvedSources := make(map[string]struct{}, len(files))
	resolvedTargets := make(map[string]struct{}, len(files))
	for _, file := range files {
		source, resolveErr := ws.Resolve(ctx, file.path, file.operation != "delete")
		if resolveErr != nil {
			return "", resolveErr
		}
		_, repeated := resolvedSources[source]
		if repeated {
			return "", fmt.Errorf("patch repeats source file: %s", file.path)
		}
		resolvedSources[source] = struct{}{}
		if file.operation != "add" {
			sourceExists, existsErr := patcher.FileExists(ctx, source)
			if existsErr != nil {
				return "", existsErr
			}
			if !sourceExists {
				return "", fmt.Errorf("patch source does not exist: %s", file.path)
			}
		}
		target := source
		if file.moveTo != "" {
			target, resolveErr = ws.Resolve(ctx, file.moveTo, true)
			if resolveErr != nil {
				return "", resolveErr
			}
			if source == target {
				return "", fmt.Errorf("patch moves %s onto itself", file.path)
			}
		}
		if file.operation == "add" || file.moveTo != "" {
			_, repeatedTarget := resolvedTargets[target]
			if repeatedTarget {
				return "", fmt.Errorf("patch repeats destination: %s", target)
			}
			resolvedTargets[target] = struct{}{}
			targetExists, targetErr := patcher.FileExists(ctx, target)
			if targetErr != nil {
				return "", targetErr
			}
			if targetExists {
				targetName := file.path
				if file.moveTo != "" {
					targetName = file.moveTo
				}
				return "", fmt.Errorf("%w: patch destination already exists: %s", ErrAlreadyExists, targetName)
			}
		}
		content := ""
		if file.operation == "update" {
			limit := 1 << 30
			content, err = ws.Read(ctx, file.path, nil, &limit)
			if err != nil {
				return "", err
			}
		}
		switch file.operation {
		case "add":
			if len(file.lines) == 0 {
				return "", fmt.Errorf("empty add file: %s", file.path)
			}
			for _, line := range file.lines {
				if !strings.HasPrefix(line, "+") {
					return "", fmt.Errorf("invalid add line in %s", file.path)
				}
				content += strings.TrimPrefix(line, "+") + "\n"
			}
		case "update":
			content, err = applyPatchHunks(content, file.lines)
			if err != nil {
				return "", fmt.Errorf("patch %s: %w", file.path, err)
			}
		case "delete":
			if len(file.lines) != 0 {
				return "", fmt.Errorf("delete file has content: %s", file.path)
			}
		}
		changes = append(changes, change{file.operation, file.path, file.moveTo, content})
	}
	var results []string
	for _, change := range changes {
		contextErr := ctx.Err()
		if contextErr != nil {
			return "", contextErr
		}
		if change.op == "delete" {
			_, deleteErr := ws.Delete(ctx, change.path)
			if deleteErr != nil {
				return "", deleteErr
			}
			results = append(results, "D "+change.path)
			continue
		}
		target := change.path
		if change.moveTo != "" {
			target = change.moveTo
		}
		var result *WriteResult
		if change.op == "add" || change.moveTo != "" {
			result, err = patcher.CreateFileNoReplace(ctx, target, change.content)
		} else {
			result, err = ws.Write(ctx, target, change.content)
		}
		if err != nil {
			return "", err
		}
		if result != nil && result.Error != "" {
			return "", fmt.Errorf("patch write %s: %s", target, result.Error)
		}
		if change.moveTo != "" {
			_, deleteErr := ws.Delete(ctx, change.path)
			if deleteErr != nil {
				return "", deleteErr
			}
		}
		if change.op == "add" {
			results = append(results, "A "+target)
		} else {
			results = append(results, "M "+target)
		}
	}
	return strings.Join(results, "\n"), nil
}

func parseWorkspacePatch(raw string) ([]patchFile, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var files []patchFile
	var current *patchFile
	ended := false
	for _, line := range lines[1:] {
		if ended {
			if line != "" {
				return nil, fmt.Errorf("unexpected content after patch end")
			}
			continue
		}
		if line == "*** End Patch" {
			if current != nil {
				files = append(files, *current)
			}
			ended = true
			continue
		}
		var op, name string
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			op, name = "add", strings.TrimPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "*** Update File: "):
			op, name = "update", strings.TrimPrefix(line, "*** Update File: ")
		case strings.HasPrefix(line, "*** Delete File: "):
			op, name = "delete", strings.TrimPrefix(line, "*** Delete File: ")
		}
		if op != "" {
			if current != nil {
				files = append(files, *current)
			}
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("patch file path is required")
			}
			current = &patchFile{operation: op, path: name}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("patch content before file header")
		}
		if strings.HasPrefix(line, "*** Move to: ") && current.operation == "update" {
			current.moveTo = strings.TrimPrefix(line, "*** Move to: ")
			if current.moveTo == "" {
				return nil, fmt.Errorf("move target is required")
			}
			continue
		}
		if strings.HasPrefix(line, "***") && line != "*** End of File" {
			return nil, fmt.Errorf("unknown patch marker: %s", line)
		}
		current.lines = append(current.lines, line)
	}
	if !ended {
		return nil, fmt.Errorf("patch is missing *** End Patch")
	}
	return files, nil
}

func applyPatchHunks(content string, lines []string) (string, error) {
	if len(lines) == 0 {
		return "", fmt.Errorf("update has no hunks")
	}
	var result strings.Builder
	cursor := 0
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "@@") {
			return "", fmt.Errorf("expected @@ hunk header")
		}
		anchor := strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))
		i++
		if anchor != "" {
			at := strings.Index(content[cursor:], anchor)
			if at < 0 {
				return "", fmt.Errorf("hunk anchor not found: %s", anchor)
			}
			result.WriteString(content[cursor : cursor+at])
			cursor += at
		}
		var old, next strings.Builder
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			line := lines[i]
			i++
			if line == "*** End of File" {
				continue
			}
			if line == "" {
				return "", fmt.Errorf("invalid empty hunk line")
			}
			switch line[0] {
			case ' ':
				old.WriteString(line[1:] + "\n")
				next.WriteString(line[1:] + "\n")
			case '-':
				old.WriteString(line[1:] + "\n")
			case '+':
				next.WriteString(line[1:] + "\n")
			default:
				return "", fmt.Errorf("invalid hunk line: %s", line)
			}
		}
		needle := old.String()
		at := strings.Index(content[cursor:], needle)
		if at < 0 && strings.HasSuffix(needle, "\n") && strings.HasSuffix(content, strings.TrimSuffix(needle, "\n")) {
			needle = strings.TrimSuffix(needle, "\n")
			at = strings.Index(content[cursor:], needle)
		}
		if at < 0 {
			return "", fmt.Errorf("hunk context not found")
		}
		result.WriteString(content[cursor : cursor+at])
		result.WriteString(next.String())
		cursor += at + len(needle)
	}
	result.WriteString(content[cursor:])
	return result.String(), nil
}
