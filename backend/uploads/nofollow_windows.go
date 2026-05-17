//go:build windows

package uploads

// Windows OpenFile has no O_NOFOLLOW; Lstat checks are best-effort.
func osNoFollow() int { return 0 }
