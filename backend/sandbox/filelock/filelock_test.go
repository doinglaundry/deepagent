package filelock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireReleaseSerialises(t *testing.T) {
	key := Key{SandboxID: "x", Path: "/p"}
	var counter int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := Acquire(key)
			defer release()
			n := atomic.AddInt32(&counter, 1)
			if n != 1 {
				t.Errorf("concurrent holders: %d", n)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&counter, -1)
		}()
	}
	wg.Wait()
}

func TestAcquireDropsEntryWhenRefZero(t *testing.T) {
	key := Key{SandboxID: "y", Path: "/q"}
	release := Acquire(key)
	release()

	guard.Lock()
	_, exists := locks[key]
	guard.Unlock()
	if exists {
		t.Fatal("expected map to be cleaned up after ref=0")
	}
}
