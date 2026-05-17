package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/sandbox"
)

// sandboxGrep dispatches to Sandbox.Grep using the subset of grepArgs flags
// the sandbox interface understands (pattern, glob, -i, head_limit). Falls
// back to ok=false on any error so the legacy runGrep path keeps the model
// behaviour stable when advanced flags (-A/-B/-C, file type, multiline) are
// requested.
func sandboxGrep(ctx context.Context, sb sandbox.Sandbox, in grepArgs) (string, bool) {
	if in.OutputMode != "" && in.OutputMode != "files_with_matches" {
		return "", false
	}
	if in.Multiline != nil && *in.Multiline {
		return "", false
	}
	if in.FileType != nil && *in.FileType != "" {
		return "", false
	}
	path := *in.Path
	opts := sandbox.GrepOpts{
		Literal:       false,
		CaseSensitive: !boolOr(in.CaseInsensitive, false),
	}
	if in.Glob != nil {
		opts.Glob = *in.Glob
	}
	if in.HeadLimit != nil {
		opts.MaxResults = *in.HeadLimit
	}
	matches, _, err := sb.Grep(ctx, path, in.Pattern, opts)
	if err != nil {
		return "", false
	}
	if len(matches) == 0 {
		return "No matches found.", true
	}
	uniq := map[string]struct{}{}
	for _, m := range matches {
		uniq[m.Path] = struct{}{}
	}
	files := make([]string, 0, len(uniq))
	for p := range uniq {
		files = append(files, p)
	}
	sort.Strings(files)
	return fmt.Sprintf("Found %d file(s)\n%s", len(files), strings.Join(files, "\n")), true
}

func boolOr(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

// grepArgs mirrors eino filesystem.go:688-740 byte-for-byte. Many fields
// are accepted-and-ignored at runtime (glob / type / multiline / -A/-B/-C)
// to keep the JSON schema 1:1 with what the model is trained on.
type grepArgs struct {
	Pattern         string  `json:"pattern"                  jsonschema:"description=The regular expression pattern to search for in file contents"`
	Path            *string `json:"path,omitempty"           jsonschema:"description=File or directory to search in (rg PATH). Defaults to current working directory."`
	Glob            *string `json:"glob,omitempty"           jsonschema:"description=Glob pattern to filter files (e.g. '*.js'\\, '*.{ts\\,tsx}') - maps to rg --glob"`
	OutputMode      string  `json:"output_mode,omitempty"    jsonschema:"description=Output mode: 'content' shows matching lines (supports -A/-B/-C context\\, -n line numbers\\, head_limit)\\, 'files_with_matches' shows file paths (supports head_limit)\\, 'count' shows match counts (supports head_limit). Defaults to 'files_with_matches'.,enum=content,enum=files_with_matches,enum=count"`
	Context         *int    `json:"-C,omitempty"             jsonschema:"description=Number of lines to show before and after each match (rg -C). Requires output_mode: 'content'\\, ignored otherwise."`
	BeforeLines     *int    `json:"-B,omitempty"             jsonschema:"description=Number of lines to show before each match (rg -B). Requires output_mode: 'content'\\, ignored otherwise."`
	AfterLines      *int    `json:"-A,omitempty"             jsonschema:"description=Number of lines to show after each match (rg -A). Requires output_mode: 'content'\\, ignored otherwise."`
	ShowLineNumbers *bool   `json:"-n,omitempty"             jsonschema:"description=Show line numbers in output (rg -n). Requires output_mode: 'content'\\, ignored otherwise. Defaults to true."`
	CaseInsensitive *bool   `json:"-i,omitempty"             jsonschema:"description=Case insensitive search (rg -i)"`
	FileType        *string `json:"type,omitempty"           jsonschema:"description=File type to search (rg --type). Common types: js\\, py\\, rust\\, go\\, java\\, etc. More efficient than include for standard file types."`
	HeadLimit       *int    `json:"head_limit,omitempty"     jsonschema:"description=Limit output to first N lines/entries\\, equivalent to '| head -N'. Works across all output modes: content (limits output lines)\\, files_with_matches (limits file paths)\\, count (limits count entries). Defaults to 0 (unlimited)."`
	Offset          *int    `json:"offset,omitempty"         jsonschema:"description=Skip first N lines/entries before applying head_limit\\, equivalent to '| tail -n +N | head -N'. Works across all output modes. Defaults to 0."`
	Multiline       *bool   `json:"multiline,omitempty"      jsonschema:"description=Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false."`
}

type grepMatch struct {
	Path    string
	Line    int
	Content string
}

// GetGrepTool returns the "grep" tool. Output formats per OutputMode mirror
// eino:
//   - "" / "files_with_matches": "Found N file(s)\n<paths>"
//   - "content": "<path>:<line>:<content>" lines (or "<path>:<content>" when -n=false)
//   - "count": "<path>:<count>" lines + summary footer
func GetGrepTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameGrep, filesystem.GrepToolDesc,
		func(ctx context.Context, in grepArgs) (string, error) {
			// Sandbox fast-path: only kick in when the model explicitly
			// asked for /mnt/... and a sandbox is wired. Other flags
			// (Multiline / FileType / offset / -A / -B / -C / OutputMode)
			// stay on the legacy path — runGrep's behaviour is what the
			// model is trained on.
			if in.Path != nil && shouldUseSandbox(*in.Path) {
				if sb := sandboxFromCtx(ctx); sb != nil {
					if out, ok := sandboxGrep(ctx, sb, in); ok {
						return out, nil
					}
				}
			}
			matches, err := runGrep(root, in)
			if err != nil {
				return "", err
			}
			sort.SliceStable(matches, func(i, j int) bool {
				return filepath.Base(matches[i].Path) < filepath.Base(matches[j].Path)
			})

			offset := valueOr(in.Offset, 0)
			headLimit := valueOr(in.HeadLimit, 0)
			showLine := valueOr(in.ShowLineNumbers, true)

			switch in.OutputMode {
			case "content":
				return formatGrepContent(applyPagination(matches, offset, headLimit), showLine), nil
			case "count":
				return formatGrepCount(matches, offset, headLimit), nil
			default:
				return formatGrepFiles(matches, offset, headLimit), nil
			}
		})
}

// runGrep walks searchRoot, compiles pattern (with (?i:) prefix when
// CaseInsensitive is set), returns one grepMatch per matched line. Empty
// in.Path falls back to root via resolvePath's join-with-empty semantics.
func runGrep(root string, in grepArgs) ([]grepMatch, error) {
	pattern := in.Pattern
	if valueOr(in.CaseInsensitive, false) {
		pattern = "(?i:" + pattern + ")"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	base := resolveRoot(root)
	searchRoot := resolvePath(root, valueOr(in.Path, ""))

	var matches []grepMatch
	walkErr := filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, grepMatch{Path: rel, Line: i + 1, Content: line})
			}
		}
		return nil
	})
	return matches, walkErr
}

func formatGrepFiles(matches []grepMatch, offset, headLimit int) string {
	if len(matches) == 0 {
		return noFilesFound
	}
	seen := make(map[string]bool, len(matches))
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m.Path] {
			seen[m.Path] = true
			paths = append(paths, m.Path)
		}
	}
	total := len(paths)
	paths = applyPagination(paths, offset, headLimit)
	return fmt.Sprintf("Found %d %s\n%s", total, plural(total, "file", "files"), strings.Join(paths, "\n"))
}

func formatGrepContent(matches []grepMatch, showLine bool) string {
	if len(matches) == 0 {
		return noMatchesFound
	}
	lines := make([]string, len(matches))
	for i, m := range matches {
		if showLine {
			lines[i] = fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Content)
		} else {
			lines[i] = m.Path + ":" + m.Content
		}
	}
	return strings.Join(lines, "\n")
}

func formatGrepCount(matches []grepMatch, offset, headLimit int) string {
	countMap := make(map[string]int)
	for _, m := range matches {
		countMap[m.Path]++
	}
	paths := make([]string, 0, len(countMap))
	for p := range countMap {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	totalOccurrences := len(matches)
	totalFiles := len(paths)
	summary := fmt.Sprintf("Found %d total %s across %d %s.",
		totalOccurrences, plural(totalOccurrences, "occurrence", "occurrences"),
		totalFiles, plural(totalFiles, "file", "files"))
	if totalOccurrences == 0 {
		return noMatchesFound + "\n\n" + summary
	}

	paths = applyPagination(paths, offset, headLimit)
	lines := make([]string, len(paths))
	for i, p := range paths {
		lines[i] = fmt.Sprintf("%s:%d", p, countMap[p])
	}
	return strings.Join(lines, "\n") + "\n\n" + summary
}

// applyPagination drops the first offset items then caps to headLimit.
// headLimit <= 0 means "no cap". Generic so it works for both grepMatch and
// plain []string (paths) slices.
func applyPagination[T any](items []T, offset, headLimit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	items = items[offset:]
	if headLimit > 0 && headLimit < len(items) {
		items = items[:headLimit]
	}
	return items
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// valueOr returns *p if p is non-nil, otherwise fallback. One generic
// replaces per-type deref helpers; reads as a phrase ("value or fallback").
func valueOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
