package admin

import (
	"encoding/json"
	"fmt"
	"github.com/fudomunro/aegis/internal/kinds"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/ownership"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

// ownershipStatuses derives the per-kind ownership view: user confirmation
// joined with live evidence. Fail-open throughout — a store or ledger that
// cannot be read simply drops its evidence from the derivation.
func (h *Handlers) OwnershipStatuses() []shared.OwnershipStatus {
	kindList := h.KindCatalog.AllLibraryKinds()
	sources, routes := h.ownershipEvidence()
	freshness := h.ownershipFreshnessByKind()

	entries := map[string]ownership.OwnershipEntry{}
	if h.Ownership != nil {
		entries = h.Ownership.List()
	}

	out := make([]shared.OwnershipStatus, 0, len(kindList))
	for _, kind := range kindList {
		st := shared.OwnershipStatus{
			Kind:            kind.ID,
			Label:           kind.Label,
			UnderManagement: sources[kind.ID],
			UnusedRoutes:    routes[kind.ID],
			Freshness:       freshness[kind.ID],
		}
		if st.UnderManagement == nil {
			st.UnderManagement = []shared.OwnershipSource{}
		}
		if st.UnusedRoutes == nil {
			st.UnusedRoutes = []shared.OwnershipRoute{}
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
func (h *Handlers) ownershipEvidence() (map[string][]shared.OwnershipSource, map[string][]shared.OwnershipRoute) {
	sources := map[string][]shared.OwnershipSource{}
	routes := map[string][]shared.OwnershipRoute{}

	maturity := connectors.MaturityByService()
	use := h.ownershipServiceUse()

	// Connector-backed services: configured or catalog-only.
	seen := map[string]bool{}
	for _, svc := range h.Host.ServiceRows() {
		if strings.EqualFold(svc.Action, "block") {
			continue
		}
		seen[svc.ID] = true
		conn := h.Connectors[svc.ID]
		if conn == nil {
			continue
		}
		u := use[svc.ID]
		name := ownershipServiceName(svc, conn)
		for _, kind := range h.KindCatalog.AllLibraryKinds() {
			if !h.FlowProvidesKind(conn, kind.ID) {
				continue
			}
			if u.inUse {
				src := shared.OwnershipSource{
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
		for _, dt := range m.Kinds {
			if !dt.Supported {
				continue
			}
			kind := dt.Name
			if kinds.BuiltinKindByID(kind) == nil {
				continue
			}
			u := use[id]
			if u.inUse {
				sources[kind] = append(sources[kind], shared.OwnershipSource{
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
	h.ownershipMailEvidence(sources, routes)

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
func (h *Handlers) ownershipServiceUse() map[string]ownershipUse {
	out := map[string]ownershipUse{}
	var tokenStatus map[string]tokens.TokenStatus
	if h.Tokens != nil {
		tokenStatus = h.Tokens.Status()
	}

	for _, svc := range h.Host.ServiceRows() {
		connID := h.Host.ConnectionIDForService(svc.ID)
		st := ownershipUse{}
		if t, ok := tokenStatus[connID]; ok && t.HasToken {
			st.inUse = true
			st.detail = "connected"
		} else if svc.StaticCredentialLabel != "" {
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
	if h.Signal != nil && h.Signal.Enabled() {
		contacts, messages, err := h.Signal.Counts()
		if err == nil {
			markArchive(connectors.SignalServiceID, contacts+messages,
				fmt.Sprintf("%d contacts, %d messages imported", contacts, messages))
		}
	}
	if h.Quicken != nil && h.Quicken.Enabled() {
		n, err := h.Quicken.Count()
		if err == nil {
			markArchive(connectors.QuickenServiceID, n,
				fmt.Sprintf("%d transactions imported", n))
		}
	}
	if h.Timeline != nil && h.Timeline.Enabled() {
		n, err := h.Timeline.Count()
		if err == nil {
			markArchive(connectors.TimelineServiceID, n,
				fmt.Sprintf("%d place visits imported", n))
		}
	}
	if h.LinkedIn != nil && h.LinkedIn.Enabled() {
		n, err := h.LinkedIn.Counts()
		if err == nil {
			markArchive(connectors.LinkedInServiceID, n,
				fmt.Sprintf("%d connections imported", n))
		}
	}
	return out
}

// ownershipMailEvidence folds mail-archive ingest sources into the
// messages view. Gmail enriches its connector row; Takeout is a source of
// its own (issue #286, issue #139).
func (h *Handlers) ownershipMailEvidence(sources map[string][]shared.OwnershipSource, routes map[string][]shared.OwnershipRoute) {
	if h.Mail == nil {
		return
	}
	st, err := h.Mail.Status()
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
			sources["messages"] = append(sources["messages"], shared.OwnershipSource{
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
		sources["messages"] = append(sources["messages"], shared.OwnershipSource{
			ServiceID: connectors.TakeoutServiceID,
			Name:      name,
			Detail:    detail,
			Count:     takeoutRows,
		})
		return
	}
	action := shared.OwnershipAction{Kind: shared.OwnershipActionImport, Target: connectors.TakeoutServiceID}
	if start := connectors.MailSourceFor(connectors.TakeoutServiceID).Start; start != nil {
		action = *start
		if action.Target == "" {
			action.Target = connectors.TakeoutServiceID
		}
	}
	routes["messages"] = append(routes["messages"], shared.OwnershipRoute{
		ServiceID:   connectors.TakeoutServiceID,
		Name:        name,
		Transport:   string(connectors.TransportArchive),
		Description: "Historical mail export (.mbox / .zip) imported into the local mail archive",
		Action:      action,
	})
}

// ownershipRoute builds one unused-route entry from the connector-declared
// maturity row (issue #254): transport, per-type description, and the
// declared call-to-action. Core never derives the action from interfaces.
func ownershipRoute(serviceID, name string, m connectors.ConnectionMaturity, kind string) shared.OwnershipRoute {
	r := shared.OwnershipRoute{
		ServiceID: serviceID,
		Name:      name,
		Transport: string(m.Transport),
		Action:    m.Start,
	}
	// A declared action may omit Target; the source's service id is the
	// default anchor (setup id, import panel prefix).
	if r.Action.Target == "" {
		r.Action.Target = serviceID
	}
	for _, dt := range m.Kinds {
		if dt.Name == kind {
			r.Description = dt.Description
			break
		}
	}
	return r
}

// ownershipServiceName resolves the display name: per-instance config label
// first, then the connector-declared registry (issue #254).
func ownershipServiceName(svc ServiceFacts, conn connectors.ServiceConnector) string {
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
func ownershipRole(svc ServiceFacts, kind string) string {
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
func (h *Handlers) ownershipFreshnessByKind() map[string]*shared.OwnershipFreshness {
	out := map[string]*shared.OwnershipFreshness{}
	if h.Copies == nil {
		return out
	}
	for _, f := range h.Copies.FreshnessRollup() {
		f := f
		out[f.Kind] = &shared.OwnershipFreshness{
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
func (h *Handlers) HandleAdminOwnership(w http.ResponseWriter, r *http.Request) {
	kind := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/ownership"), "/")
	switch {
	case kind == "" && r.Method == http.MethodGet:
		shared.WriteJSON(w, map[string]any{"ownership": h.OwnershipStatuses()})
	case kind != "" && r.Method == http.MethodPost:
		h.handleOwnershipConfirm(w, r, kind)
	case kind != "" && r.Method == http.MethodDelete:
		h.handleOwnershipReopen(w, r, kind)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handlers) handleOwnershipConfirm(w http.ResponseWriter, r *http.Request, kind string) {
	if kinds.BuiltinKindByID(kind) == nil {
		if _, ok := h.KindCatalog.UDKSchemaForKind(kind); !ok {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown kind "+kind)
			return
		}
	}
	if h.Ownership == nil {
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
	if _, err := h.Ownership.Confirm(kind, req.Note); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to save ownership confirmation")
		return
	}
	h.writeOwnershipStatus(w, kind)
}

func (h *Handlers) handleOwnershipReopen(w http.ResponseWriter, r *http.Request, kind string) {
	if kinds.BuiltinKindByID(kind) == nil {
		if _, ok := h.KindCatalog.UDKSchemaForKind(kind); !ok {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown kind "+kind)
			return
		}
	}
	if h.Ownership == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "ownership store disabled")
		return
	}
	if _, err := h.Ownership.Reopen(kind); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to save ownership state")
		return
	}
	h.writeOwnershipStatus(w, kind)
}

// writeOwnershipStatus answers with the refreshed single-kind view so a
// confirm/reopen does not need a second fetch (the onboarding-runner
// pattern: recompute after applying the update).
func (h *Handlers) writeOwnershipStatus(w http.ResponseWriter, kind string) {
	for _, st := range h.OwnershipStatuses() {
		if st.Kind == kind {
			shared.WriteJSON(w, st)
			return
		}
	}
	shared.WriteJSONError(w, http.StatusNotFound, "unknown kind "+kind)
}
