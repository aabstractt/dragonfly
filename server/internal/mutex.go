package internal

import (
	"github.com/cosmos72/gls"
	"sync"
	"sync/atomic"
)

type RecursivePanicMutex struct {
	mu      sync.Mutex
	holding int64
}

func (mu *RecursivePanicMutex) Lock() {
	id := int64(gls.GoID())
	if atomic.LoadInt64(&mu.holding) == id {
		panic("locked data is already being changed on this goroutine. Panicking to prevent recursive lock")
	}
	mu.mu.Lock()
	atomic.StoreInt64(&mu.holding, id)
}

func (mu *RecursivePanicMutex) Unlock() {
	atomic.StoreInt64(&mu.holding, -1)
	mu.mu.Unlock()
}
