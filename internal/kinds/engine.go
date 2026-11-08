package kinds

import (
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/shared"
)

// Cache is the engine's cache surface. The hub's cache store satisfies it.
type Cache interface {
	Get(key string) (string, bool)
	Set(key, value string, ttl time.Duration)
}

// Engine owns the aggregate pipeline's shared state: the merged-aggregate
// cache, fetch coalescing, and the search index with its write-behind
// WaitGroup (issue #312). Core constructs it.
type Engine struct {
	cache   Cache
	search  *search.Index
	flight  shared.FlightGroup[any]
	indexWG sync.WaitGroup
}

// NewEngine wires an engine over the hub's cache and search index. The
// index may be nil or disabled; search routes answer 503 then.
func NewEngine(cache Cache, idx *search.Index) *Engine {
	return &Engine{cache: cache, search: idx}
}

// WaitIndex blocks until scheduled search-index write-behind work finishes.
// Tests use it so a list fetch can materialize the index before querying.
func (e *Engine) WaitIndex() {
	e.indexWG.Wait()
}
