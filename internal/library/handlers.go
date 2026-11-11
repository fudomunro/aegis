package library

import (
	"net/http"
	"sync"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/udk"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library/links"
	"github.com/fudomunro/aegis/internal/shared"
)

// Handlers is the agent data plane: kind composition, route map, fixed
// write/maintenance routes, content transport, and agent-facing copy. The
// hub constructs it once, mounts RouteHandlers behind the auth perimeter,
// and calls the exported copy builders from the pre-request hook.
type Handlers struct {
	// Host is the hub-side seam the data plane needs at request time — the
	// same narrow runtime view the kinds pipeline uses (authorization maps,
	// service facts, access recording, logging, identity). Core implements
	// it; credentials stay behind connectors.Host on the hub side.
	Host kinds.Host
	// Deps are the kind-constructor args the hub supplies (mail archive,
	// entity resolvers, local-source probes).
	Deps kinds.Deps
	// Cache is the shared response cache (library keys are prefixed
	// "library:").
	Cache *shared.Cache
	// Search is the search index engine behind kind search; it may be nil
	// or disabled.
	Search kinds.SearchIndex
	// Links is the user-confirmed entity-link store.
	Links *links.Store
	// Copies is the durable copy ledger.
	Copies *copies.Ledger
	// UDKs is the user-defined-kind registry (schema packs + overlay).
	UDKs *udk.Registry
	// BaseURL is the instance base URL for kind instruction text.
	BaseURL string
	// Connectors is the configured connector registry (typed, for mirror
	// and content-transport provider lookups).
	Connectors map[string]connectors.ServiceConnector
	// MirrorSourceFn / MirrorTargetFn override the mirror adapter lookup.
	// Nil in production; tests use them to stub a provider.
	MirrorSourceFn func(serviceID, kind string) (MirrorSource, error)
	MirrorTargetFn func(serviceID, kind string) (MirrorTarget, error)

	eng          *kinds.Engine
	engineOnce   sync.Once
	kindList     []kinds.Kind
	kindListOnce sync.Once
}

// Engine returns the aggregate pipeline engine. Fixtures that skip engine
// wiring get one lazily so a zero-value Handlers fails open instead of
// panicking (issue #312).
func (h *Handlers) Engine() *kinds.Engine {
	h.engineOnce.Do(func() {
		if h.eng == nil {
			cache := h.Cache
			if cache == nil {
				cache = shared.NewCache()
			}
			h.eng = kinds.NewEngine(cache, h.Search)
		}
	})
	return h.eng
}

// WaitIndex blocks until scheduled search-index write-behind work finishes.
func (h *Handlers) WaitIndex() {
	h.Engine().WaitIndex()
}

// RouteHandlers maps every library route to its HTTP handler so the hub
// can register the composed kind registry (built-ins plus user-defined,
// shadow-checked at composition) plus the fixed cross-kind entries.
func (h *Handlers) RouteHandlers() map[string]http.HandlerFunc {
	handlers := map[string]http.HandlerFunc{}
	for _, k := range h.KindRegistry() {
		for _, route := range k.Routes() {
			handlers[route.Pattern] = route.Handler
		}
	}
	// Cross-kind maintenance reads (copy ledger, merge review, entity
	// links) are not owned by any kind: fixed registrations here, and the
	// API-listener deny list names the admin-side ones explicitly.
	handlers["/v1/library/copies"] = h.HandleLibraryCopies
	handlers["/v1/library/review"] = h.HandleMergeReview
	handlers["/v1/library/links"] = h.HandleEntityLinks
	handlers["/v1/library/links/"] = h.HandleEntityLinks
	return handlers
}
