package shared

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlightGroup_DoSharesResult(t *testing.T) {
	var f FlightGroup[string]
	var calls atomic.Int32
	fn := func() (string, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "ok", nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := f.Do("k", fn)
			if err != nil || tok != "ok" {
				t.Errorf("Do = (%q, %v)", tok, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fn calls = %d, want 1", calls.Load())
	}
}
