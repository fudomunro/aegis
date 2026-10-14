package core

import "sync"

// tokenFlight coalesces concurrent refresh/exchange work for the same key
// (provider connection ID) so one upstream call serves every waiter.
type tokenFlight struct {
	mu    sync.Mutex
	calls map[string]*tokenFlightCall
}

type tokenFlightCall struct {
	wg  sync.WaitGroup
	tok *OAuthToken
	err error
}

func (f *tokenFlight) Do(key string, fn func() (*OAuthToken, error)) (*OAuthToken, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]*tokenFlightCall)
	}
	if c, ok := f.calls[key]; ok {
		f.mu.Unlock()
		c.wg.Wait()
		return c.tok, c.err
	}
	c := &tokenFlightCall{}
	c.wg.Add(1)
	f.calls[key] = c
	f.mu.Unlock()

	c.tok, c.err = fn()
	c.wg.Done()

	f.mu.Lock()
	delete(f.calls, key)
	f.mu.Unlock()
	return c.tok, c.err
}
