package sourceview

import (
	"runtime"
	"sync"
)

// forEachParallel calls fn(i) for every i in [0, n) on a bounded pool of
// goroutines and returns once every call has finished. Each call must write
// only state owned by its index; callers consume the results in index order,
// so the outcome — including which error is reported first — is the same as
// a sequential loop.
func forEachParallel(n int, fn func(i int)) {
	workers := min(n, max(4, runtime.GOMAXPROCS(0)))
	if workers <= 1 {
		for i := range n {
			fn(i)
		}
		return
	}
	var next sync.Mutex
	cursor := 0
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				next.Lock()
				i := cursor
				cursor++
				next.Unlock()
				if i >= n {
					return
				}
				fn(i)
			}
		})
	}
	wg.Wait()
}
