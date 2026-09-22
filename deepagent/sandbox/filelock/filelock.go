// Package filelock per-(sandboxID, path) mutex with refcount so the map drains.
package filelock

import "sync"

// Key uniquely identifies a file inside a sandbox.
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

// Acquire locks key and returns a release func the caller must defer.
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
