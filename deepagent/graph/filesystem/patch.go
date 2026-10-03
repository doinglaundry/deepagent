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
func ApplyWorkspacePatch(ctx context.Context, filesystem Filesystem, rawPatch string) (string, error) {
	patchFiles, err := parseWorkspacePatch(rawPatch)
	if err != nil {
		return "", err
	}
	patchFilesystem, ok := filesystem.(patchFilesystem)
	if !ok {
		return "", fmt.Errorf("filesystem does not support safe patch creation")
	}
	type change struct{ op, path, moveTo, content string }
	changes := make([]change, 0, len(patchFiles))
	resolvedSources := make(map[string]struct{}, len(patchFiles))
	resolvedTargets := make(map[string]struct{}, len(patchFiles))
	for _, patchFile := range patchFiles {
		sourcePath, resolveErr := filesystem.Resolve(ctx, patchFile.path, patchFile.operation != "delete")
		if resolveErr != nil {
			return "", resolveErr
		}
		_, repeated := resolvedSources[sourcePath]
		if repeated {
			return "", fmt.Errorf("patch repeats source file: %s", patchFile.path)
		}
		resolvedSources[sourcePath] = struct{}{}
		if patchFile.operation != "add" {
			sourceExists, existsErr := patchFilesystem.HasFile(ctx, sourcePath)
			if existsErr != nil {
				return "", existsErr
			}
			if !sourceExists {
				return "", fmt.Errorf("patch source does not exist: %s", patchFile.path)
			}
		}
		targetPath := sourcePath
		if patchFile.moveTo != "" {
			targetPath, resolveErr = filesystem.Resolve(ctx, patchFile.moveTo, true)
			if resolveErr != nil {
				return "", resolveErr
			}
			if sourcePath == targetPath {
				return "", fmt.Errorf("patch moves %s onto itself", patchFile.path)
			}
		}
		if patchFile.operation == "add" || patchFile.moveTo != "" {
			_, repeatedTarget := resolvedTargets[targetPath]
			if repeatedTarget {
				return "", fmt.Errorf("patch repeats destination: %s", targetPath)
			}
			resolvedTargets[targetPath] = struct{}{}
			targetExists, targetErr := patchFilesystem.HasFile(ctx, targetPath)
			if targetErr != nil {
				return "", targetErr
			}
			if targetExists {
				destinationName := patchFile.path
				if patchFile.moveTo != "" {
					destinationName = patchFile.moveTo
				}
				return "", fmt.Errorf("%w: patch destination already exists: %s", ErrAlreadyExists, destinationName)
			}
		}
		content := ""
		if patchFile.operation == "update" {
			readLimit := 1 << 30
			content, err = filesystem.Read(ctx, patchFile.path, nil, &readLimit)
			if err != nil {
				return "", err
			}
		}
		switch patchFile.operation {
		case "add":
			if len(patchFile.lines) == 0 {
				return "", fmt.Errorf("empty add file: %s", patchFile.path)
			}
			for _, line := range patchFile.lines {
				if !strings.HasPrefix(line, "+") {
					return "", fmt.Errorf("invalid add line in %s", patchFile.path)
				}
				content += strings.TrimPrefix(line, "+") + "\n"
			}
		case "update":
			content, err = applyPatchHunks(content, patchFile.lines)
			if err != nil {
				return "", fmt.Errorf("patch %s: %w", patchFile.path, err)
			}
		case "delete":
			if len(patchFile.lines) != 0 {
				return "", fmt.Errorf("delete file has content: %s", patchFile.path)
			}
		}
		changes = append(changes, change{patchFile.operation, patchFile.path, patchFile.moveTo, content})
	}
	var results []string
	for _, fileChange := range changes {
		contextErr := ctx.Err()
		if contextErr != nil {
			return "", contextErr
		}
		if fileChange.op == "delete" {
			_, deleteErr := filesystem.Delete(ctx, fileChange.path)
			if deleteErr != nil {
				return "", deleteErr
			}
			results = append(results, "D "+fileChange.path)
			continue
		}
		targetPath := fileChange.path
		if fileChange.moveTo != "" {
			targetPath = fileChange.moveTo
		}
		var writeResult *WriteResult
		if fileChange.op == "add" || fileChange.moveTo != "" {
			writeResult, err = patchFilesystem.CreateFileNoReplace(ctx, targetPath, fileChange.content)
		} else {
			writeResult, err = filesystem.Write(ctx, targetPath, fileChange.content)
		}
		if err != nil {
			return "", err
		}
		if writeResult != nil && writeResult.Error != "" {
			return "", fmt.Errorf("patch write %s: %s", targetPath, writeResult.Error)
		}
		if fileChange.moveTo != "" {
			_, deleteErr := filesystem.Delete(ctx, fileChange.path)
			if deleteErr != nil {
				return "", deleteErr
			}
		}
		if fileChange.op == "add" {
			results = append(results, "A "+targetPath)
		} else {
			results = append(results, "M "+targetPath)
		}
	}
	return strings.Join(results, "\n"), nil
}

func parseWorkspacePatch(rawPatch string) ([]patchFile, error) {
	patchLines := strings.Split(strings.ReplaceAll(rawPatch, "\r\n", "\n"), "\n")
	if len(patchLines) < 2 || patchLines[0] != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var patchFiles []patchFile
	var currentPatchFile *patchFile
	ended := false
	for _, line := range patchLines[1:] {
		if ended {
			if line != "" {
				return nil, fmt.Errorf("unexpected content after patch end")
			}
			continue
		}
		if line == "*** End Patch" {
			if currentPatchFile != nil {
				patchFiles = append(patchFiles, *currentPatchFile)
			}
			ended = true
			continue
		}
		var operation, filePath string
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			operation, filePath = "add", strings.TrimPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "*** Update File: "):
			operation, filePath = "update", strings.TrimPrefix(line, "*** Update File: ")
		case strings.HasPrefix(line, "*** Delete File: "):
			operation, filePath = "delete", strings.TrimPrefix(line, "*** Delete File: ")
		}
		if operation != "" {
			if currentPatchFile != nil {
				patchFiles = append(patchFiles, *currentPatchFile)
			}
			if strings.TrimSpace(filePath) == "" {
				return nil, fmt.Errorf("patch file path is required")
			}
			currentPatchFile = &patchFile{operation: operation, path: filePath}
			continue
		}
		if currentPatchFile == nil {
			return nil, fmt.Errorf("patch content before file header")
		}
		if strings.HasPrefix(line, "*** Move to: ") && currentPatchFile.operation == "update" {
			currentPatchFile.moveTo = strings.TrimPrefix(line, "*** Move to: ")
			if currentPatchFile.moveTo == "" {
				return nil, fmt.Errorf("move target is required")
			}
			continue
		}
		if strings.HasPrefix(line, "***") && line != "*** End of File" {
			return nil, fmt.Errorf("unknown patch marker: %s", line)
		}
		currentPatchFile.lines = append(currentPatchFile.lines, line)
	}
	if !ended {
		return nil, fmt.Errorf("patch is missing *** End Patch")
	}
	return patchFiles, nil
}

func applyPatchHunks(content string, hunkLines []string) (string, error) {
	if len(hunkLines) == 0 {
		return "", fmt.Errorf("update has no hunks")
	}
	var updatedContent strings.Builder
	contentOffset := 0
	for i := 0; i < len(hunkLines); {
		if !strings.HasPrefix(hunkLines[i], "@@") {
			return "", fmt.Errorf("expected @@ hunk header")
		}
		anchor := strings.TrimSpace(strings.TrimPrefix(hunkLines[i], "@@"))
		i++
		if anchor != "" {
			matchOffset := strings.Index(content[contentOffset:], anchor)
			if matchOffset < 0 {
				return "", fmt.Errorf("hunk anchor not found: %s", anchor)
			}
			updatedContent.WriteString(content[contentOffset : contentOffset+matchOffset])
			contentOffset += matchOffset
		}
		var oldTextBuilder, newTextBuilder strings.Builder
		for i < len(hunkLines) && !strings.HasPrefix(hunkLines[i], "@@") {
			line := hunkLines[i]
			i++
			if line == "*** End of File" {
				continue
			}
			if line == "" {
				return "", fmt.Errorf("invalid empty hunk line")
			}
			switch line[0] {
			case ' ':
				oldTextBuilder.WriteString(line[1:] + "\n")
				newTextBuilder.WriteString(line[1:] + "\n")
			case '-':
				oldTextBuilder.WriteString(line[1:] + "\n")
			case '+':
				newTextBuilder.WriteString(line[1:] + "\n")
			default:
				return "", fmt.Errorf("invalid hunk line: %s", line)
			}
		}
		oldText := oldTextBuilder.String()
		matchOffset := strings.Index(content[contentOffset:], oldText)
		if matchOffset < 0 && strings.HasSuffix(oldText, "\n") && strings.HasSuffix(content, strings.TrimSuffix(oldText, "\n")) {
			oldText = strings.TrimSuffix(oldText, "\n")
			matchOffset = strings.Index(content[contentOffset:], oldText)
		}
		if matchOffset < 0 {
			return "", fmt.Errorf("hunk context not found")
		}
		updatedContent.WriteString(content[contentOffset : contentOffset+matchOffset])
		updatedContent.WriteString(newTextBuilder.String())
		contentOffset += matchOffset + len(oldText)
	}
	updatedContent.WriteString(content[contentOffset:])
	return updatedContent.String(), nil
}
