package autodream

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultMinHours = 24
	holderStale     = time.Hour
	lockFileName    = ".consolidate-lock"
)

type ConsolidationLock struct {
	memoryRoot         string
	previousModTime    time.Time
	previousWasMissing bool
}

func getLockPath(memoryRoot string) string {
	return filepath.Join(memoryRoot, lockFileName)
}

func ReadLastConsolidatedAt(memoryRoot string) (time.Time, error) {
	info, err := os.Stat(getLockPath(memoryRoot))
	if err != nil && !os.IsNotExist(err) {
		return time.Time{}, err
	}

	if os.IsNotExist(err) {
		return time.Time{}, nil
	}

	return info.ModTime(), nil
}

func ShouldPassTimeGate(lastConsolidatedAt time.Time) bool {
	now := time.Now()
	return lastConsolidatedAt.IsZero() || now.Sub(lastConsolidatedAt) >= defaultMinHours*time.Hour
}

func isProcessRunning(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func TryAcquireConsolidationLock(memoryRoot string) (lock *ConsolidationLock, err error) {
	now := time.Now()
	err = os.MkdirAll(memoryRoot, 0o755)
	if err != nil {
		return nil, err
	}

	path := getLockPath(memoryRoot)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil && !os.IsExist(err) {
		return nil, err
	}

	//此时创建新的lockfile成功，说明之前没有创建过lockfile
	if err == nil {
		defer f.Close()
		_, err = fmt.Fprintln(f, os.Getpid())
		if err != nil {
			return nil, err
		}
		if err = os.Chtimes(path, now, now); err != nil {
			return nil, err
		}

		return &ConsolidationLock{
			memoryRoot:         memoryRoot,
			previousModTime:    time.Time{},
			previousWasMissing: true,
		}, nil
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pid, _ := strconv.Atoi(strings.TrimSpace(string(payload)))

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	previousLockModTime := info.ModTime()

	if isProcessRunning(pid) && now.Sub(previousLockModTime) < holderStale {
		return nil, nil
	}

	err = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	if err != nil {
		return nil, err
	}

	err = os.Chtimes(path, now, now)
	if err != nil {
		return nil, err
	}

	return &ConsolidationLock{
		memoryRoot:         memoryRoot,
		previousModTime:    previousLockModTime,
		previousWasMissing: false,
	}, nil
}

func RollbackConsolidationLock(lock *ConsolidationLock) {
	if lock == nil {
		return
	}
	path := getLockPath(lock.memoryRoot)
	if lock.previousWasMissing {
		_ = os.Remove(path)
		return
	}
	_ = os.Chtimes(path, lock.previousModTime, lock.previousModTime)
}
