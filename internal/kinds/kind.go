package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

// Route is one agent-tier GET/HEAD route owned by a library kind. The
// declaration carries the route's exposure: the API-listener and app-token
// allowlists derive from it. Kinds declare reads only — writes (mirror,
// photos/share) stay admin-registered in core (issue #312).
type Route struct {
	// Pattern is the ServeMux pattern: the kind path, or a kind subroute
	// like "/v1/library/documents/{service}/{id}/text".
	Pattern string
	// Handler is bound to the instance Host and Engine at construction.
	Handler http.HandlerFunc
	// AppReadable marks third-party app-token exposure (freebusy: yes;
	// document text: no).
	AppReadable bool
	// GrantKey names the route in a data grant: the library-relative route
	// path (issue #348), e.g. "freebusy" for /v1/library/freebusy. A grant
	// map key is a route — empty means the kind's PermType.
	GrantKey string
}

// Kind is one library kind owned by this package (issue #312): its data
// entry, its declared routes, and the visibility predicates core consults.
// Adding a kind is one file plus one line in Builtin — core does not change.
type Kind interface {
	// Def is the kind registry entry (see librarykinds.go).
	Def() LibraryKind
	// Routes are the agent-tier GET/HEAD routes the kind owns.
	Routes() []Route
	// Provides reports whether a connector instance feeds this kind.
	Provides(connector any) bool
	// WriteCapable reports whether the connector can receive writes for
	// this kind (issue #365) — the gate for designating a sync hub and for
	// converging a member in the other direction.
	WriteCapable(connector any) bool
	// HasProviders reports whether any source can feed this kind.
	HasProviders(h Host) bool
	// Readable reports whether at least one non-blocked service allows
	// reads at owner tier.
	Readable(h Host) bool
}

// Deps are per-instance constructor arguments kinds need beyond Host
// (issue #312): store handles and resolvers that are not Host concerns —
// the NewQuickenConnector pattern.
type Deps struct {
	// PlaylistLinkKey resolves confirmed cross-service playlist links
	// (issue #4).
	PlaylistLinkKey func(Playlist) string
	// MusicBrainz resolvers attach durable IDs before merge (issue #4).
	ResolveTracks  func(context.Context, []Track)
	ResolveAlbums  func(context.Context, []Album)
	ResolveArtists func(context.Context, []Artist)
	// PlacesLocalSource reports whether the Timeline import store can feed
	// the places kind without a live connector (issue #142) — the declared
	// local-source shape, not a pipeline branch (issue #312).
	PlacesLocalSource func() bool
	// MailArchive is the messages kind's declared local collection source
	// (issue #139). Nil disables it.
	MailArchive MailArchive
}

// Builtin returns the built-in kinds this package owns, in registry order.
// Explicit wiring like srv.connectors — no init() registration (issue
// #312). Kinds not yet moved out of the core hub stay off this list until
// their move PR; the list only grows.
func Builtin(h Host, e *Engine, d Deps) []Kind {
	return []Kind{
		newPlaylistsKind(h, e, d),
		newTracksKind(h, e, d),
		newAlbumsKind(h, e, d),
		newArtistsKind(h, e, d),
		newContactsKind(h, e, d),
		newPhotosKind(h, e, d),
		newEventsKind(h, e, d),
		newPlacesKind(h, e, d),
		newDocumentsKind(h, e, d),
		newTransactionsKind(h, e, d),
		newMessagesKind(h, e, d),
	}
}

// builtinDecls is the static route-declaration table (pattern + exposure,
// no handlers) built from the same constructors Builtin uses. Kind
// declarations are static data, so a nil-Host pass is safe — constructors
// must not dereference the Host at construction (issue #312).
var builtinDecls = func() []Route {
	var out []Route
	for _, k := range Builtin(nil, nil, Deps{}) {
		for _, r := range k.Routes() {
			out = append(out, Route{Pattern: r.Pattern, AppReadable: r.AppReadable, GrantKey: r.GrantKey})
		}
	}
	return out
}()

// RouteDecls returns every built-in kind route declaration without
// instance-bound handlers — the table the pure API-listener and app-token
// perimeter checks derive from (issue #312).
func RouteDecls() []Route {
	return builtinDecls
}

// providerKind is the common shape for kinds fed by connectors
// implementing a provider interface (the newKindRuntime[P] pattern).
type providerKind struct {
	def         LibraryKind
	routes      []Route
	provides    func(any) bool
	writeCapable func(any) bool
}

func (k providerKind) Def() LibraryKind            { return k.def }
func (k providerKind) Routes() []Route             { return k.routes }
func (k providerKind) Provides(connector any) bool { return k.provides(connector) }

// WriteCapable reports whether connector can receive writes for this kind.
// Kinds without a write provider are read-only everywhere.
func (k providerKind) WriteCapable(connector any) bool {
	return k.writeCapable != nil && k.writeCapable(connector)
}

// HasProviders reports whether any registered connector provides P.
func (k providerKind) HasProviders(h Host) bool {
	for _, c := range h.Connectors() {
		if k.provides(c) {
			return true
		}
	}
	return false
}

// Readable reports whether at least one non-blocked service allows reads
// (owner-tier effective permissions; the libraryReadableFor predicate).
func (k providerKind) Readable(h Host) bool {
	return readableFor(h, k.def.PermType)
}

// readableFor reports whether at least one non-blocked service allows
// GET at owner tier for kind.
func readableFor(h Host, kind string) bool {
	level := h.OwnerGrants().Level(kind)
	for _, svc := range h.Services() {
		if svc.Action == "block" {
			continue
		}
		if shared.AllowsMethod(level, http.MethodGet) {
			return true
		}
	}
	return false
}

// providerProvides builds a Provides predicate from a provider-interface
// assertion, e.g. providerProvides[PlaylistProvider].
func providerProvides[P any](connector any) bool {
	_, ok := connector.(P)
	return ok
}
