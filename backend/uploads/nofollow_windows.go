//go:build windows

package uploads

// osNoFollow: Windows OpenFile does not support O_NOFOLLOW. Returning 0
// keeps the upload path working; defence-in-depth on Windows is
// best-effort via Lstat checks before open.
func osNoFollow() int { return 0 }
