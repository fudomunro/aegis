package connectors

// Local import store registry (issue #393): one declaration per
// file-based import store, consumed generically by core (flags, data
// paths, CLI import mode, connector registration), admin (routes,
// ownership evidence), and the admin UI (panels rendered from declared
// copy). Adding a store is a file beside its store plus one line in
// LocalImportStoreSpecs() — the hub never names a store.
//
// Explicit wiring only: this list is the ordered literal, the same
// class as kinds.Builtin() and core's srv.connectors. No init().

// LocalImportStoreSpec is the complete wiring declaration for one local
// import store: how it is named, where it lives on disk, how the operator
// invokes it, and how core constructs its connector and maturity row.
// Every provider-facing string stays with the store itself (the Spec
// constructor in the store's file, and the store's Admin() copy) — this
// type is the seam, not a place for prose.
type LocalImportStoreSpec struct {
	// Service is the in-memory config card injected while the store is
	// enabled (LocalStoreService).
	Service LocalStoreService
	// Stem is the operator-facing short name used for flags, the admin
	// route, and the default file name: "signal" → -signal-store,
	// -import-signal, /v1/admin/signal, signal.db. Not the service ID
	// (timeline's stem is "timeline" while its service is
	// "google-timeline").
	Stem string
	// DefaultFile is the store file name under the data directory.
	DefaultFile string
	// StoreFlagHelp is the -<stem>-store flag help text.
	StoreFlagHelp string
	// ImportFlagHelp is the -import-<stem> flag help text.
	ImportFlagHelp string
	// Open opens (or creates) the store at path. An empty path returns a
	// disabled store (nil, nil) — typed-nil behind the interface, which
	// the interface methods must tolerate.
	Open func(path string) (ImportStore, error)
	// NewConnector builds the ServiceConnector over the store. Passed the
	// typed store from Open (possibly a typed nil when disabled).
	NewConnector func(ImportStore) ServiceConnector
	// Maturity is the hand-declared coverage row for the connection
	// maturity catalog (issue #247).
	Maturity func() ConnectionMaturity
}

// AdminPrefix is the admin route prefix for this store, e.g.
// "/v1/admin/signal".
func (s LocalImportStoreSpec) AdminPrefix() string {
	return "/v1/admin/" + s.Stem
}

// StoreFlagName is the disabling flag, e.g. "-signal-store".
func (s LocalImportStoreSpec) StoreFlagName() string {
	return "-" + s.Stem + "-store"
}

// ImportFlagName is the one-shot import flag, e.g. "-import-signal".
func (s LocalImportStoreSpec) ImportFlagName() string {
	return "-import-" + s.Stem
}

// LocalImportStoreSpecs returns the ordered registry of local import
// stores. One line per store; new stores append here and nowhere else in
// the hub.
func LocalImportStoreSpecs() []LocalImportStoreSpec {
	return []LocalImportStoreSpec{
		quickenStoreSpec(),
		timelineStoreSpec(),
		signalStoreSpec(),
		linkedinStoreSpec(),
	}
}

// LocalImportStoreSpecFor returns the spec for a stem, or false.
func LocalImportStoreSpecFor(stem string) (LocalImportStoreSpec, bool) {
	for _, s := range LocalImportStoreSpecs() {
		if s.Stem == stem {
			return s, true
		}
	}
	return LocalImportStoreSpec{}, false
}
