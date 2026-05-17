// Package search is the algorithmic core shared by LocalSandbox.Glob / Grep.
package search

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// IgnorePatterns are the dir/file names Glob/Grep skip.
var IgnorePatterns = []string{
	".git", ".svn", ".hg", ".bzr",
	"node_modules", "__pycache__",
	".venv", "venv", ".env", "env",
	".tox", ".nox", ".eggs", "*.egg-info", "site-packages",
	"dist", "build", ".next", ".nuxt", ".output", ".turbo", "target", "out",
	".idea", ".vscode",
	"*.swp", "*.swo", "*~",
	".project", ".classpath", ".settings",
	".DS_Store", "Thumbs.db", "desktop.ini", "*.lnk",
	"*.log", "*.tmp", "*.temp", "*.bak", "*.cache",
	".cache", "logs",
	".coverage", "coverage", ".nyc_output", "htmlcov",
	".pytest_cache", ".mypy_cache", ".ruff_cache",
}

const (
	defaultMaxFileSizeBytes   = 1_000_000
	defaultLineSummaryLength  = 200
	defaultGlobMaxResults     = 200
	defaultGrepMaxResults     = 100
	maxLineCharsRatio         = 10
)

// GrepMatch is the leaf type Grep returns; mirrors sandbox.GrepMatch to avoid import cycle.
type GrepMatch struct {
	Path       string
	LineNumber int
	Line       string
}

// ShouldIgnoreName reports whether name matches any IgnorePatterns entry.
func ShouldIgnoreName(name string) bool {
	for _, pattern := range IgnorePatterns {
		if ok, _ := doublestar.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// PathMatches reports whether relPath matches pattern; accepts bare or "**/" prefixed.
func PathMatches(pattern, relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	if ok, _ := doublestar.PathMatch(pattern, relPath); ok {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		ok, _ := doublestar.PathMatch(pattern[3:], relPath)
		return ok
	}
	return false
}

func truncateLine(line string, maxChars int) string {
	line = strings.TrimRight(line, "\r\n")
	if len(line) <= maxChars {
		return line
	}
	return line[:maxChars-3] + "..."
}

// isBinary samples the first bytes for a NUL — same heuristic as file(1).
func isBinary(path string, sampleSize int) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, sampleSize)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return true
	}
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// FindGlobMatches walks root and returns paths matching pattern; truncated=hit cap.
func FindGlobMatches(root, pattern string, opts GlobOpts) ([]string, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	st, err := os.Stat(rootAbs)
	if err != nil {
		return nil, false, err
	}
	if !st.IsDir() {
		return nil, false, &fs.PathError{Op: "glob", Path: rootAbs, Err: errors.New("not a directory")}
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = defaultGlobMaxResults
	}

	var (
		matches   []string
		truncated bool
	)
	stopErr := errors.New("stop")

	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p == rootAbs {
			return nil
		}
		if ShouldIgnoreName(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(rootAbs, p)
		if err != nil {
			return nil
		}
		if d.IsDir() && !opts.IncludeDirs {
			return nil
		}
		if PathMatches(pattern, rel) {
			matches = append(matches, p)
			if len(matches) >= maxResults {
				truncated = true
				return stopErr
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, stopErr) {
		return matches, truncated, walkErr
	}
	return matches, truncated, nil
}

// FindGrepMatches walks root for lines matching pattern; skips symlinks/binaries/oversized files.
func FindGrepMatches(root, pattern string, opts GrepOpts) ([]GrepMatch, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	st, err := os.Stat(rootAbs)
	if err != nil {
		return nil, false, err
	}
	if !st.IsDir() {
		return nil, false, &fs.PathError{Op: "grep", Path: rootAbs, Err: errors.New("not a directory")}
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = defaultGrepMaxResults
	}
	maxFileSize := opts.MaxFileSizeBytes
	if maxFileSize <= 0 {
		maxFileSize = defaultMaxFileSizeBytes
	}
	lineSummaryLength := opts.LineSummaryLength
	if lineSummaryLength <= 0 {
		lineSummaryLength = defaultLineSummaryLength
	}
	maxLineChars := lineSummaryLength * maxLineCharsRatio

	source := pattern
	if opts.Literal {
		source = regexp.QuoteMeta(pattern)
	}
	if !opts.CaseSensitive {
		source = "(?i)" + source
	}
	re, err := regexp.Compile(source)
	if err != nil {
		return nil, false, err
	}

	var (
		matches   []GrepMatch
		truncated bool
	)
	stopErr := errors.New("stop")

	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p == rootAbs {
			return nil
		}
		if ShouldIgnoreName(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(rootAbs, p)
		if err != nil {
			return nil
		}
		if opts.Glob != "" && !PathMatches(opts.Glob, rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > int64(maxFileSize) {
			return nil
		}
		if isBinary(p, 8192) {
			return nil
		}
		fileMatches, hit, err := scanFile(p, re, maxLineChars, lineSummaryLength, maxResults-len(matches))
		if err != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		if hit || len(matches) >= maxResults {
			truncated = true
			return stopErr
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, stopErr) {
		return matches, truncated, walkErr
	}
	return matches, truncated, nil
}

// scanFile reads path line-by-line via bufio.Reader (Scanner's buffer cap would break minified files).
func scanFile(path string, re *regexp.Regexp, maxLineChars, lineSummaryLength, remaining int) ([]GrepMatch, bool, error) {
	if remaining <= 0 {
		return nil, true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	out := make([]GrepMatch, 0, 8)
	r := bufio.NewReader(f)

	for lineNum := 1; ; lineNum++ {
		line, err := r.ReadString('\n')
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			return out, false, err
		}
		if len(line) > maxLineChars {
			if eof {
				break
			}
			continue
		}
		if re.MatchString(line) {
			out = append(out, GrepMatch{
				Path:       path,
				LineNumber: lineNum,
				Line:       truncateLine(line, lineSummaryLength),
			})
			if len(out) >= remaining {
				return out, true, nil
			}
		}
		if eof {
			break
		}
	}
	return out, false, nil
}

// GlobOpts mirrors sandbox.GlobOpts here to avoid the import cycle.
type GlobOpts struct {
	IncludeDirs bool
	MaxResults  int
}

// GrepOpts is the algorithm-internal grep config.
type GrepOpts struct {
	Glob              string
	Literal           bool
	CaseSensitive     bool
	MaxResults        int
	MaxFileSizeBytes  int
	LineSummaryLength int
}
