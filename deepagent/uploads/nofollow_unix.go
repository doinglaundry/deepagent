//go:build unix

package uploads

import "syscall"

// osNoFollow returns O_NOFOLLOW so OpenFile refuses a symlink at dest.
func osNoFollow() int { return syscall.O_NOFOLLOW }
