//go:build unix

package uploads

import "syscall"

// osNoFollow returns the O_NOFOLLOW flag on POSIX so OpenFile refuses to
// traverse a symlink at the destination. Windows build (nofollow_windows.go)
// returns 0 because the syscall isn't available there.
func osNoFollow() int { return syscall.O_NOFOLLOW }
