package agentthread

import (
	"sync"
	"time"
)

const modelBarrierWaitWarnThreshold = 50 * time.Millisecond

type modelEventBarrier struct {
	done chan struct{}
	once sync.Once
}

func newModelEventBarrier() *modelEventBarrier { return &modelEventBarrier{done: make(chan struct{})} }
func (b *modelEventBarrier) release() {
	if b == nil {
		return
	}
	b.once.Do(func() { close(b.done) })
}
