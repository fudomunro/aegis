package shared

import "sync"

// FlightGroup coalesces concurrent work for the same key so one call serves
// every waiter (token refresh per connection, library aggregates per cache
// key).
type FlightGroup[T any] struct {
	mu    sync.Mutex
	calls map[string]*flightCall[T]
}

type flightCall[T any] struct {
	wg  sync.WaitGroup
	val T
	err error
}

func (f *FlightGroup[T]) Do(key string, fn func() (T, error)) (T, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]*flightCall[T])
	}
	if c, ok := f.calls[key]; ok {
		f.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &flightCall[T]{}
	c.wg.Add(1)
	f.calls[key] = c
	f.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	f.mu.Lock()
	delete(f.calls, key)
	f.mu.Unlock()
	return c.val, c.err
}
