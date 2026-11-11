package kinds

import (
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// Cache is the engine's cache surface. The hub's cache store satisfies it.
type Cache interface {
	Get(key string) (string, bool)
	Set(key, value string, ttl time.Duration)
}

// SearchIndex is the search-engine seam behind kind search: the concrete
// local index is wired hub-side (core/search). Rows are shared.SearchDocument
// values built by BuildSearchDocument from the kind's SearchSpec.
type SearchIndex interface {
	// Enabled reports whether the index is usable; search routes answer
	// 503 when it is not.
	Enabled() bool
	// Count returns the number of indexed documents for (kind, dataset).
	Count(kind, dataset string) (int, error)
	// Search runs one full-text lookup over a kind.
	Search(q shared.SearchQuery) (shared.SearchResult, error)
	// Sync stores a complete fetch: rows not in docs and not in complete
	// are refuted. complete names the services whose fetch was complete.
	Sync(kind, dataset string, docs []shared.SearchDocument, complete []string) error
}

// Engine owns the aggregate pipeline's shared state: the merged-aggregate
// cache, fetch coalescing, and the search index with its write-behind
// WaitGroup (issue #312). Core constructs it.
type Engine struct {
	cache   Cache
	search  SearchIndex
	flight  shared.FlightGroup[any]
	indexWG sync.WaitGroup
}

// NewEngine wires an engine over the hub's cache and search index. The
// index may be nil or disabled; search routes answer 503 then.
func NewEngine(cache Cache, idx SearchIndex) *Engine {
	return &Engine{cache: cache, search: idx}
}

// WaitIndex blocks until scheduled search-index write-behind work finishes.
// Tests use it so a list fetch can materialize the index before querying.
func (e *Engine) WaitIndex() {
	e.indexWG.Wait()
}
