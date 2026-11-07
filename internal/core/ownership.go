package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// Data-ownership state (issue #165): per-kind user confirmation that Aegis
// already holds all the data the user cares about, plus the derived
// evidence behind it — which sources are in use, which declared routes are
// still unused, and the copy-freshness rollup. Until a kind is confirmed,
// the admin surface keeps a call to action to manage more data of that kind.
//
// Confirmation is the only user-authored state (ownership.json). Sources and
// routes are derived from the connection-maturity catalog, ingest-source
// declarations, configured services, and local store counts — the same
// declared metadata the sync dashboard reads (issue #254).

// ownershipFileVersion is the on-disk schema version for ownership.json.
const ownershipFileVersion = 1

// ownershipFile is the on-disk format.
type ownershipFile struct {
	Version   int                             `json:"version"`
	Ownership map[string]types.OwnershipEntry `json:"ownership"`
}

// OwnershipStore is a mutex-guarded map of kind id → user confirmation.
type OwnershipStore struct {
	mu     sync.RWMutex
	path   string
	byKind map[string]types.OwnershipEntry
}

// NewOwnershipStore loads ownership.json. A missing file yields an empty
// store — no kind is confirmed until the user says so. An empty path
// disables persistence (entries stay in memory for the process lifetime).
func NewOwnershipStore(path string) (*OwnershipStore, error) {
	s := &OwnershipStore{
		path:   path,
		byKind: make(map[string]types.OwnershipEntry),
	}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var file ownershipFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse ownership.json: %w", err)
	}
	if file.Version != 0 && file.Version != ownershipFileVersion {
		return nil, fmt.Errorf("unsupported ownership.json version %d", file.Version)
	}
	for kind, entry := range file.Ownership {
		if kind == "" {
			return nil, fmt.Errorf("ownership: entry missing kind id")
		}
		s.byKind[kind] = entry
	}
	return s, nil
}

// Get returns the confirmation entry for a kind. The zero value means
// unconfirmed.
func (s *OwnershipStore) Get(kind string) types.OwnershipEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byKind[kind]
}

// List returns a copy of all entries keyed by kind id.
func (s *OwnershipStore) List() map[string]types.OwnershipEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]types.OwnershipEntry, len(s.byKind))
	for k, v := range s.byKind {
		out[k] = v
	}
	return out
}

// Confirm records the user's statement that the kind is complete. A
// non-empty note is stored beside it; confirmed_at is stamped now.
func (s *OwnershipStore) Confirm(kind, note string) (types.OwnershipEntry, error) {
	if strings.TrimSpace(kind) == "" {
		return types.OwnershipEntry{}, fmt.Errorf("kind is required")
	}
	entry := types.OwnershipEntry{
		Confirmed:   true,
		ConfirmedAt: time.Now().UTC().Format(time.RFC3339),
		Note:        strings.TrimSpace(note),
	}
	s.mu.Lock()
	prev, existed := s.byKind[kind]
	s.byKind[kind] = entry
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		// Roll back so memory and disk do not disagree.
		s.mu.Lock()
		if existed {
			s.byKind[kind] = prev
		} else {
			delete(s.byKind, kind)
		}
		s.mu.Unlock()
		return types.OwnershipEntry{}, err
	}
	return entry, nil
}

// Reopen clears a confirmation, returning the kind to the call-to-action
// state. The note is kept so the user's context survives a reopen.
func (s *OwnershipStore) Reopen(kind string) (types.OwnershipEntry, error) {
	if strings.TrimSpace(kind) == "" {
		return types.OwnershipEntry{}, fmt.Errorf("kind is required")
	}
	s.mu.Lock()
	prev, existed := s.byKind[kind]
	entry := types.OwnershipEntry{Note: prev.Note}
	s.byKind[kind] = entry
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if existed {
			s.byKind[kind] = prev
		} else {
			delete(s.byKind, kind)
		}
		s.mu.Unlock()
		return types.OwnershipEntry{}, err
	}
	return entry, nil
}

// persist writes ownership.json atomically. No-op when persistence is
// disabled (empty path).
func (s *OwnershipStore) persist() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	file := ownershipFile{
		Version:   ownershipFileVersion,
		Ownership: make(map[string]types.OwnershipEntry, len(s.byKind)),
	}
	for k, v := range s.byKind {
		file.Ownership[k] = v
	}
	s.mu.RUnlock()
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return shared.AtomicWriteFile(s.path, append(raw, '\n'), 0o600)
}

// ownershipStatuses derives the per-kind ownership view: user confirmation
// joined with live evidence. Fail-open throughout — a store or ledger that
// cannot be read simply drops its evidence from the derivation.
func (s *Server) ownershipStatuses() []types.OwnershipStatus {
	kinds := s.allLibraryKinds()
	sources, routes := s.ownershipEvidence()
	freshness := s.ownershipFreshnessByKind()

	entries := map[string]types.OwnershipEntry{}
	if s.ownership != nil {
		entries = s.ownership.List()
	}

	out := make([]types.OwnershipStatus, 0, len(kinds))
	for _, kind := range kinds {
		st := types.OwnershipStatus{
			Kind:            kind.ID,
			Label:           kind.Label,
			UnderManagement: sources[kind.ID],
			UnusedRoutes:    routes[kind.ID],
			Freshness:       freshness[kind.ID],
		}
		if st.UnderManagement == nil {
			st.UnderManagement = []types.OwnershipSource{}
		}
		if st.UnusedRoutes == nil {
			st.UnusedRoutes = []types.OwnershipRoute{}
		}
		if e, ok := entries[kind.ID]; ok {
			st.Confirmed = e.Confirmed
			st.ConfirmedAt = e.ConfirmedAt
			st.Note = e.Note
		}
		out = append(out, st)
	}
	return out
}

// ownershipEvidence returns per-kind sources currently in use and declared
// routes that are not. "In use" means the source is actually managing data:
// a live connection with credentials, or an archive source that holds
// imported rows. Everything else that can feed the kind stays a route.
func (s *Server) ownershipEvidence() (map[string][]types.OwnershipSource, map[string][]types.OwnershipRoute) {
	sources := map[string][]types.OwnershipSource{}
	routes := map[string][]types.OwnershipRoute{}

	maturity := MaturityByService()
	use := s.ownershipServiceUse()

	// Connector-backed services: configured or catalog-only.
	seen := map[string]bool{}
	for _, svc := range s.config.ServiceSnapshots() {
		if strings.EqualFold(svc.Action, "block") {
			continue
		}
		seen[svc.ID] = true
		conn := s.connectors[svc.ID]
		if conn == nil {
			continue
		}
		u := use[svc.ID]
		name := ownershipServiceName(svc, conn)
		for _, kind := range s.allLibraryKinds() {
			if !flowProvidesKind(conn, kind.ID) {
				continue
			}
			if u.inUse {
				src := types.OwnershipSource{
					ServiceID:    svc.ID,
					Name:         name,
					Role:         ownershipRole(svc, kind.ID),
					Count:        u.count,
					LastSyncedAt: u.lastSynced,
					Detail:       u.detail,
				}
				sources[kind.ID] = append(sources[kind.ID], src)
			} else {
				routes[kind.ID] = append(routes[kind.ID], ownershipRoute(svc.ID, name, maturity[svc.ID], kind.ID))
			}
		}
	}

	// Catalog-only services: known connectors the user has not added yet.
	for id, m := range maturity {
		if seen[id] {
			continue
		}
		name := ownershipCatalogName(id)
		for _, dt := range m.DataTypes {
			if !dt.Supported {
				continue
			}
			kind := dt.Name
			if LibraryKindByID(kind) == nil {
				continue
			}
			u := use[id]
			if u.inUse {
				sources[kind] = append(sources[kind], types.OwnershipSource{
					ServiceID: id,
					Name:      name,
					Count:     u.count,
					Detail:    u.detail,
				})
			} else {
				routes[kind] = append(routes[kind], ownershipRoute(id, name, m, kind))
			}
		}
	}

	// Mail-archive ingest sources (issue #286) are evidence the maturity
	// catalog does not carry as separate connections.
	s.ownershipMailEvidence(sources, routes)

	for kind := range sources {
		sort.Slice(sources[kind], func(i, j int) bool {
			return sources[kind][i].Name < sources[kind][j].Name
		})
	}
	for kind := range routes {
		sort.Slice(routes[kind], func(i, j int) bool {
			return routes[kind][i].Name < routes[kind][j].Name
		})
	}
	return sources, routes
}

// ownershipUse is one service's evidence of managing data.
type ownershipUse struct {
	inUse      bool
	count      int
	lastSynced string
	detail     string
}

// ownershipServiceUse computes per-service evidence: a live connection with
// credentials is in use; an archive transport is in use only once it holds
// imported rows, so an empty Signal or Quicken store stays a call to action.
func (s *Server) ownershipServiceUse() map[string]ownershipUse {
	out := map[string]ownershipUse{}
	var tokenStatus map[string]TokenStatus
	if s.tokens != nil {
		tokenStatus = s.tokens.Status()
	}

	for _, svc := range s.config.ServiceSnapshots() {
		connID := s.config.connectionIDForService(svc.ID)
		st := ownershipUse{}
		if t, ok := tokenStatus[connID]; ok && t.HasToken {
			st.inUse = true
			st.detail = "connected"
		} else if staticCredentialLabel(svc) != "" {
			st.inUse = true
			st.detail = "connected"
		}
		out[svc.ID] = st
	}

	// Archive transports only count once they hold rows.
	markArchive := func(id string, n int, detail string) {
		st := out[id]
		if n > 0 {
			st.inUse = true
			st.count = n
			st.detail = detail
		} else if !st.inUse {
			st.detail = ""
		}
		out[id] = st
	}
	if s.signal != nil && s.signal.Enabled() {
		contacts, messages, err := s.signal.Counts()
		if err == nil {
			markArchive(connectors.SignalServiceID, contacts+messages,
				fmt.Sprintf("%d contacts, %d messages imported", contacts, messages))
		}
	}
	if s.quicken != nil && s.quicken.Enabled() {
		n, err := s.quicken.Count()
		if err == nil {
			markArchive(connectors.QuickenServiceID, n,
				fmt.Sprintf("%d transactions imported", n))
		}
	}
	if s.timeline != nil && s.timeline.Enabled() {
		n, err := s.timeline.Count()
		if err == nil {
			markArchive(connectors.TimelineServiceID, n,
				fmt.Sprintf("%d place visits imported", n))
		}
	}
	return out
}

// ownershipMailEvidence folds mail-archive ingest sources into the
// messages view. Gmail enriches its connector row; Takeout is a source of
// its own (issue #286, issue #139).
func (s *Server) ownershipMailEvidence(sources map[string][]types.OwnershipSource, routes map[string][]types.OwnershipRoute) {
	if s.mail == nil {
		return
	}
	st, err := s.mail.Status()
	if err != nil {
		return
	}

	// Enrich the Gmail connector row with archive counts when present.
	if n := st.ByService[connectors.GoogleGmailServiceID]; n > 0 {
		found := false
		for i := range sources["messages"] {
			if sources["messages"][i].ServiceID == connectors.GoogleGmailServiceID {
				sources["messages"][i].Count = n
				if sources["messages"][i].Detail == "" {
					sources["messages"][i].Detail = fmt.Sprintf("%d messages in archive", n)
				}
				found = true
				break
			}
		}
		if !found {
			sources["messages"] = append(sources["messages"], types.OwnershipSource{
				ServiceID: connectors.GoogleGmailServiceID,
				Name:      connectors.GoogleGmailServiceName,
				Count:     n,
				Detail:    fmt.Sprintf("%d messages in archive", n),
			})
		}
	}

	// Takeout is an ingest-only source: no config entry, no OAuth. Its
	// call-to-action is declared on the MailSource beside the ingest
	// (issue #165), not derived here.
	takeoutRows := st.ByService[connectors.TakeoutServiceID]
	takeoutUsed := takeoutRows > 0 || strings.TrimSpace(st.TakeoutPath) != ""
	name := connectors.TakeoutServiceName
	if takeoutUsed {
		detail := fmt.Sprintf("%d messages imported", takeoutRows)
		if st.TakeoutPath != "" {
			detail = "export at " + st.TakeoutPath
			if takeoutRows > 0 {
				detail = fmt.Sprintf("%d messages imported from %s", takeoutRows, st.TakeoutPath)
			}
		}
		sources["messages"] = append(sources["messages"], types.OwnershipSource{
			ServiceID: connectors.TakeoutServiceID,
			Name:      name,
			Detail:    detail,
			Count:     takeoutRows,
		})
		return
	}
	action := types.OwnershipAction{Kind: types.OwnershipActionImport, Target: connectors.TakeoutServiceID}
	if start := connectors.MailSourceFor(connectors.TakeoutServiceID).Start; start != nil {
		action = *start
		if action.Target == "" {
			action.Target = connectors.TakeoutServiceID
		}
	}
	routes["messages"] = append(routes["messages"], types.OwnershipRoute{
		ServiceID:   connectors.TakeoutServiceID,
		Name:        name,
		Transport:   types.TransportArchive,
		Description: "Historical mail export (.mbox / .zip) imported into the local mail archive",
		Action:      action,
	})
}

// ownershipRoute builds one unused-route entry from the connector-declared
// maturity row (issue #254): transport, per-type description, and the
// declared call-to-action. Core never derives the action from interfaces.
func ownershipRoute(serviceID, name string, m types.ConnectionMaturity, kind string) types.OwnershipRoute {
	r := types.OwnershipRoute{
		ServiceID: serviceID,
		Name:      name,
		Transport: m.Transport,
		Action:    m.Start,
	}
	// A declared action may omit Target; the source's service id is the
	// default anchor (setup id, import panel prefix).
	if r.Action.Target == "" {
		r.Action.Target = serviceID
	}
	for _, dt := range m.DataTypes {
		if dt.Name == kind {
			r.Description = dt.Description
			break
		}
	}
	return r
}

// ownershipServiceName resolves the display name: per-instance config label
// first, then the connector-declared registry (issue #254).
func ownershipServiceName(svc Service, conn connectors.ServiceConnector) string {
	if strings.TrimSpace(svc.Name) != "" {
		return svc.Name
	}
	return ownershipCatalogName(svc.ID)
}

func ownershipCatalogName(id string) string {
	if name, ok := connectors.ServiceName(id); ok {
		return name
	}
	return id
}

// ownershipRole labels the copy relationship for mirrored kinds, matching
// the library source overview (issue #16).
func ownershipRole(svc Service, kind string) string {
	for _, k := range svc.CanonicalFor {
		if k == kind {
			return "canonical"
		}
	}
	switch kind {
	case "playlists", "tracks", "albums", "artists":
		return "mirror"
	}
	return ""
}

// ownershipFreshnessByKind maps the copy-ledger freshness rollup onto kind
// ids. The ledger is optional; a disabled ledger drops the field.
func (s *Server) ownershipFreshnessByKind() map[string]*types.OwnershipFreshness {
	out := map[string]*types.OwnershipFreshness{}
	if s.copies == nil {
		return out
	}
	for _, f := range s.copies.FreshnessRollup() {
		f := f
		out[f.Kind] = &types.OwnershipFreshness{
			InSync:      f.InSync,
			Stale:       f.Stale,
			Missing:     f.Missing,
			Unchecked:   f.Unchecked,
			Total:       f.Total,
			LastChecked: f.LastChecked,
		}
	}
	return out
}

// HandleAdminOwnership serves the per-kind ownership view and the user's
// confirmation:
//
//	GET    /v1/admin/ownership            → all kinds
//	POST   /v1/admin/ownership/{kind}     → confirm {"note": "…"}
//	DELETE /v1/admin/ownership/{kind}     → reopen (clear confirmation)
func (s *Server) HandleAdminOwnership(w http.ResponseWriter, r *http.Request) {
	kind := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/ownership"), "/")
	switch {
	case kind == "" && r.Method == http.MethodGet:
		shared.WriteJSON(w, map[string]any{"ownership": s.ownershipStatuses()})
	case kind != "" && r.Method == http.MethodPost:
		s.handleOwnershipConfirm(w, r, kind)
	case kind != "" && r.Method == http.MethodDelete:
		s.handleOwnershipReopen(w, r, kind)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleOwnershipConfirm(w http.ResponseWriter, r *http.Request, kind string) {
	if LibraryKindByID(kind) == nil {
		if _, ok := s.udtSchemaForKind(kind); !ok {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown data type "+kind)
			return
		}
	}
	if s.ownership == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "ownership store disabled")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if _, err := s.ownership.Confirm(kind, req.Note); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to save ownership confirmation")
		return
	}
	s.writeOwnershipStatus(w, kind)
}

func (s *Server) handleOwnershipReopen(w http.ResponseWriter, r *http.Request, kind string) {
	if LibraryKindByID(kind) == nil {
		if _, ok := s.udtSchemaForKind(kind); !ok {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown data type "+kind)
			return
		}
	}
	if s.ownership == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "ownership store disabled")
		return
	}
	if _, err := s.ownership.Reopen(kind); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to save ownership state")
		return
	}
	s.writeOwnershipStatus(w, kind)
}

// writeOwnershipStatus answers with the refreshed single-kind view so a
// confirm/reopen does not need a second fetch (the onboarding-runner
// pattern: recompute after applying the update).
func (s *Server) writeOwnershipStatus(w http.ResponseWriter, kind string) {
	for _, st := range s.ownershipStatuses() {
		if st.Kind == kind {
			shared.WriteJSON(w, st)
			return
		}
	}
	shared.WriteJSONError(w, http.StatusNotFound, "unknown data type "+kind)
}
