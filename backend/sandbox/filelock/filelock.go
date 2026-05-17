// Package filelock gives every (sandboxID, path) pair its own sync.Mutex so
// two tools writing the same file inside the same sandbox serialise, while
// independent paths stay concurrent. Mirrors deer-flow's
// file_operation_lock.py (WeakValueDictionary + threading.Lock).
//
// Go has no WeakValueDictionary; we use a ref-counted map instead. Acquire
// bumps ref + locks; Release decrements + unlocks; when ref hits zero the
// entry is dropped so the lock table doesn't accumulate ghost mutexes for
// every file ever touched.
package filelock

import "sync"

type Key struct {
	SandboxID string
	Path      string
}

type entry struct {
	mu  sync.Mutex
	ref int
}

var (
	guard sync.Mutex
	locks = map[Key]*entry{}
)

// Acquire returns a release func that the caller MUST call via defer.
// Calling Acquire on the same key from two goroutines blocks the second
// until the first releases.
func Acquire(key Key) func() {
	guard.Lock()
	e, ok := locks[key]
	if !ok {
		e = &entry{}
		locks[key] = e
	}
	e.ref++
	guard.Unlock()

	e.mu.Lock()

	return func() {
		e.mu.Unlock()
		guard.Lock()
		e.ref--
		if e.ref == 0 {
			delete(locks, key)
		}
		guard.Unlock()
	}
}
