package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Flow topology node kinds.
const (
	FlowKindAgent   = "agent"
	FlowKindGateway = "gateway"
	FlowKindAegis   = "aegis"
	FlowKindStore   = "store"
	FlowKindService = "service"
	// FlowKindToken is one OAuth connection held inside the token vault.
	FlowKindToken = "token"
)

// Flow topology node statuses for services.
const (
	FlowStatusConnected  = "connected"
	FlowStatusNeedsAuth  = "needs-auth"
	FlowStatusExpired    = "expired"
	FlowStatusBlocked    = "blocked"
	FlowStatusConfigured = "configured"
)

// Flow edge styles used by the admin Map renderer.
const (
	FlowEdgeHook    = "hook"
	FlowEdgeAccess  = "access"
	FlowEdgeData    = "data"
	FlowEdgeMeta    = "meta"
	FlowEdgeDenied  = "denied"
	FlowEdgeLibrary = "library"
)

// FlowNode is one actor, location, or store in the data-flow graph.
type FlowNode struct {
	ID string `json:"id"`
	// Kind is agent, gateway, aegis, store, or service.
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Parent nests this node inside another (e.g. aegis inside aperture).
	Parent string `json:"parent,omitempty"`
	// Status applies mainly to services (connected, needs-auth, …).
	Status   string   `json:"status,omitempty"`
	Action   string   `json:"action,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
	Kinds    []string `json:"data_types,omitempty"`
	// Providers lists unified library kinds this service can feed.
	Providers []string `json:"providers,omitempty"`
	// Notes are short caveats (e.g. photo bytes stay upstream).
	Notes []string `json:"notes,omitempty"`
	// Hits is recent access count for this node in the stats window (issue #32).
	Hits int `json:"hits,omitempty"`
	// Agents is the distinct agent count that touched this node in the window.
	Agents int `json:"agents,omitempty"`
	// ConnectionID is the OAuth connection this service uses (issue #26 / #32).
	ConnectionID string `json:"connection_id,omitempty"`
}

// FlowEdge is a directed connection between two nodes.
type FlowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	// Style is hook, access, data, meta, denied, or library.
	Style string `json:"style"`
	// Kind is the library kind id on library edges (issue #32).
	Kind string `json:"kind,omitempty"`
}

// FlowLibrary describes one unified library endpoint and its fan-in sources.
type FlowLibrary struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Label     string   `json:"label"`
	SyncRule  string   `json:"sync_rule"`
	Providers []string `json:"providers"`
	// Hits is recent library access count in the stats window (issue #32).
	Hits int `json:"hits,omitempty"`
}

// FlowLibraryKind is one synced library kind shown on the admin map.
// Map chips are limited to library kinds Aegis merges; proxy-only
// connector surfaces are not listed here.
type FlowLibraryKind struct {
	ID string `json:"id"`
	// Label is the human name ("liked tracks").
	Label string `json:"label"`
	// Path is the library endpoint.
	Path string `json:"path,omitempty"`
	// Kind is "library" (unified endpoint), fixed as part of the
	// /v1/admin/flow read model's wire shape.
	Kind string `json:"kind"`
	// Readable is true when at least one non-blocked service allows reads.
	Readable bool `json:"readable"`
	// Providers lists non-blocked services that can supply this kind.
	Providers []string `json:"providers,omitempty"`
}

// FlowRuntime is the non-secret runtime context for the map.
type FlowRuntime struct {
	AuthMode string `json:"auth_mode"`
	BaseURL  string `json:"base_url"`
}

// Flow data-grant holder kinds (issue #425).
const (
	// FlowGrantAgent is the owner-tier agent data grants map.
	FlowGrantAgent = "agent"
	// FlowGrantApp is one third-party app's data grant (scopes).
	FlowGrantApp = "app"
	// FlowGrantInvite is one invite token's data grant (data-scope grants).
	FlowGrantInvite = "invite"
)

// FlowGrantKind is one kind entry inside an issued data grant: the level
// granted and how much of it the grant's clients used in the window.
type FlowGrantKind struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Level is the granted access level (read, write, read-write).
	Level string `json:"level"`
	// Hits is the window usage of this kind by the grant's clients.
	Hits int `json:"hits,omitempty"`
}

// FlowGrantClient is one caller identity that holds a data grant.
type FlowGrantClient struct {
	ID string `json:"id"`
	// Hits is the client's access count in the stats window.
	Hits int `json:"hits,omitempty"`
	// LastAt is the client's most recent access in the window.
	LastAt time.Time `json:"last_at,omitempty"`
	// Used lists the kinds this client touched in the window.
	Used []string `json:"used,omitempty"`
}

// FlowDataGrant is one issued data grant on the overview map (issue #425):
// its holder ("by who"), the kinds it grants ("what could be accessed"), and
// the window usage of those kinds ("what is being accessed"). The join is the
// access-metrics agent identity: "app:<id>" / "scope:<id>" holders match app
// and invite-token grants; other identities share the agent data grants map.
type FlowDataGrant struct {
	// ID is the grant holder id: "agent", "app:<id>", or "scope:<id>".
	ID string `json:"id"`
	// Kind is agent, app, or invite.
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Disabled marks a revoked/disabled app or invite token.
	Disabled bool `json:"disabled,omitempty"`
	// ReadOnly marks an invite token that may not write.
	ReadOnly bool `json:"read_only,omitempty"`
	// Clients are the caller identities holding this data grant.
	Clients []FlowGrantClient `json:"clients,omitempty"`
	// Kinds lists the granted kinds (levels other than none).
	Kinds []FlowGrantKind `json:"data_types,omitempty"`
	// Hits is the combined access count of the grant's clients.
	Hits   int       `json:"hits,omitempty"`
	LastAt time.Time `json:"last_at,omitempty"`
}

// FlowAgentDetail is one agent's access breakdown for map nodes/arrows
// (issue #114).
type FlowAgentDetail struct {
	ID   string `json:"id"`
	Hits int    `json:"hits"`
	// Services maps service id → proxy hits.
	Services map[string]int `json:"services,omitempty"`
	// Libraries maps library kind id → unified library hits.
	Libraries map[string]int `json:"libraries,omitempty"`
	// Kinds maps recorded kind → hits.
	Kinds  map[string]int `json:"data_types,omitempty"`
	LastAt time.Time      `json:"last_at,omitempty"`
}

// FlowAgentStats is the distinct-agent summary for the active window.
type FlowAgentStats struct {
	Distinct int      `json:"distinct"`
	Sample   []string `json:"sample,omitempty"`
	// Detail lists each agent with the services/data they touched (issue #114).
	Detail []FlowAgentDetail `json:"detail,omitempty"`
}

// FlowEndpointStats is recent hit counts for one service or library.
type FlowEndpointStats struct {
	Hits      int            `json:"hits"`
	Agents    int            `json:"agents"`
	CacheHits int            `json:"cache_hits,omitempty"`
	CacheMiss int            `json:"cache_misses,omitempty"`
	Kinds     map[string]int `json:"data_types,omitempty"`
}

// FlowRecentAccess is a redacted sample of recent agent access (no bodies).
type FlowRecentAccess struct {
	At      time.Time `json:"at"`
	Agent   string    `json:"agent"`
	Surface string    `json:"surface"`
	Target  string    `json:"target"`
	Kind    string    `json:"data_type,omitempty"`
	Cache   string    `json:"cache,omitempty"`
}

// FlowStats is the live access summary embedded in the admin map payload.
type FlowStats struct {
	GeneratedAt time.Time                    `json:"generated_at"`
	Window      string                       `json:"window"`
	Windows     []string                     `json:"windows"`
	Agents      FlowAgentStats               `json:"agents"`
	Services    map[string]FlowEndpointStats `json:"services"`
	Libraries   map[string]FlowEndpointStats `json:"libraries"`
	Recent      []FlowRecentAccess           `json:"recent,omitempty"`
}

// FlowStatsWindows are the selectable access-stat windows for the map.
var FlowStatsWindows = []string{"5m", "1h", "24h"}

// FlowTopology is the admin data-flow map payload (issue #25, #32).
type FlowTopology struct {
	Runtime   FlowRuntime       `json:"runtime"`
	Narrative string            `json:"narrative"`
	Nodes     []FlowNode        `json:"nodes"`
	Edges     []FlowEdge        `json:"edges"`
	Libraries []FlowLibrary     `json:"libraries"`
	Kinds     []FlowLibraryKind `json:"data_types"`
	// Grants lists the issued data grants on the map's left side
	// (issue #425): what each grants and what its clients used.
	Grants []FlowDataGrant `json:"grants"`
	// Stats is recent agent access for the requested window (issue #32).
	Stats FlowStats `json:"stats"`
}

// HandleAdminFlow returns the data-flow topology for the admin Map view.
// GET /v1/admin/flow[?window=5m|1h|24h]
func (h *Handlers) HandleAdminFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	window, windowLabel := parseFlowWindow(r.URL.Query().Get("window"))
	topo := h.buildFlowTopology(window, windowLabel)
	shared.WriteJSON(w, topo)
}

// parseFlowWindow maps the window query value to a duration and stable label.
func parseFlowWindow(raw string) (time.Duration, string) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "5m", "5min":
		return 5 * time.Minute, "5m"
	case "24h", "1d", "day":
		return 24 * time.Hour, "24h"
	default:
		return time.Hour, "1h"
	}
}

// buildFlowTopology assembles the graph from config, connectors, and tokens.
// Secrets are never included.
func (h *Handlers) buildFlowTopology(window time.Duration, windowLabel string) FlowTopology {
	tokens := h.Tokens.Status()

	nodes := []FlowNode{
		{
			ID:     "agents",
			Kind:   FlowKindAgent,
			Label:  "Data grants",
			Detail: "Issued data grants — what each client may access, and what it used",
		},
		{
			ID:     "aperture",
			Kind:   FlowKindGateway,
			Label:  "Aperture",
			Detail: "Gateway shell — pre-request hook injects Aegis URLs into agent prompts",
		},
		{
			ID:     "aegis",
			Kind:   FlowKindAegis,
			Label:  "Aegis",
			Detail: "Hook · service proxy · unified library · permissions",
			Parent: "aperture",
		},
		{
			ID:     "tokens",
			Kind:   FlowKindStore,
			Label:  "Token vault",
			Detail: "Encrypted OAuth tokens on disk",
			Parent: "aegis",
		},
		{
			ID:     "cache",
			Kind:   FlowKindStore,
			Label:  "Data cache",
			Detail: "In-memory cache of synced library kinds and proxied GETs",
			Parent: "aegis",
		},
	}

	// Search index store: local FTS index behind library q= search.
	searchNode := FlowNode{
		ID:     "search",
		Kind:   FlowKindStore,
		Label:  "Search index",
		Detail: "Local FTS index for /me/{kind}?q=",
		Parent: "aegis",
		Status: "disabled",
	}
	if h.Search.Enabled() {
		searchNode.Status = "enabled"
		if counts, err := h.Search.CountsByKind(); err == nil {
			total := 0
			for _, n := range counts {
				total += n
			}
			searchNode.Detail = fmt.Sprintf("%d docs · local FTS index for q=", total)
		}
	}
	nodes = append(nodes, searchNode)

	// List tokens currently held in the vault (connection id → status).
	// Labels use connection/service display names; secrets never appear.
	tokenIDs := make([]string, 0, len(tokens))
	for id := range tokens {
		tokenIDs = append(tokenIDs, id)
	}
	sort.Strings(tokenIDs)
	seenVault := make(map[string]bool, len(tokenIDs))
	for _, connID := range tokenIDs {
		st := tokens[connID]
		status := FlowStatusConnected
		if st.Expired {
			status = FlowStatusExpired
		}
		seenVault[connID] = true
		nodes = append(nodes, FlowNode{
			ID:     "tok:" + connID,
			Kind:   FlowKindToken,
			Label:  h.TokenDisplayName(connID),
			Detail: "OAuth token held for this connection",
			Parent: "tokens",
			Status: status,
		})
	}

	// Static header credentials (YNAB PAT, Immich API key) live in
	// service_connections.json, not the OAuth token store. Surface them in
	// the vault too so the map matches what Aegis can actually inject.
	for _, svc := range h.Host.ServiceRows() {
		if strings.EqualFold(svc.Action, "block") {
			continue
		}
		connID := h.Host.ConnectionIDForService(svc.ID)
		if seenVault[connID] {
			continue
		}
		cred := svc.StaticCredentialLabel
		if cred == "" {
			continue
		}
		seenVault[connID] = true
		nodes = append(nodes, FlowNode{
			ID:     "tok:" + connID,
			Kind:   FlowKindToken,
			Label:  h.TokenDisplayName(connID),
			Detail: cred + " held for this connection",
			Parent: "tokens",
			Status: FlowStatusConnected,
		})
	}

	edges := []FlowEdge{
		{From: "agents", To: "aperture", Label: "LLM traffic", Style: FlowEdgeHook},
		{From: "aperture", To: "aegis", Label: "POST /v1/pre-request", Style: FlowEdgeHook},
		{From: "agents", To: "aegis", Label: "proxy + library API", Style: FlowEdgeAccess},
	}

	// Library kinds this server can aggregate, with which services feed them.
	libraryProviders := make(map[string][]string)
	for _, kind := range h.KindCatalog.AllLibraryKinds() {
		libraryProviders[kind.ID] = nil
	}

	for _, svc := range h.Host.ServiceRows() {
		node := FlowNode{
			ID:       "svc:" + svc.ID,
			Kind:     FlowKindService,
			Label:    svc.Name,
			Action:   svc.Action,
			Endpoint: svc.Endpoint,
			Detail:   svc.Description,
		}

		if strings.EqualFold(svc.Action, "block") {
			node.Status = FlowStatusBlocked
			node.Detail = "Blocked in this environment"
		} else {
			connID := h.Host.ConnectionIDForService(svc.ID)
			node.ConnectionID = connID
			node.Status = flowServiceStatus(svc, tokens[connID])
		}

		connector, hasConnector := h.Connectors[svc.ID]
		if hasConnector {
			caps := connector.Capabilities()
			kindList := make([]string, 0, len(caps.Kinds))
			for _, dt := range caps.Kinds {
				kindList = append(kindList, dt.Name)
			}
			node.Kinds = kindList

			for _, kind := range h.KindCatalog.AllLibraryKinds() {
				if h.FlowProvidesKind(connector, kind.ID) {
					node.Providers = append(node.Providers, kind.ID)
					if !strings.EqualFold(svc.Action, "block") {
						libraryProviders[kind.ID] = append(libraryProviders[kind.ID], svc.ID)
					}
				}
			}
			if _, ok := connector.(kinds.PhotoProvider); ok {
				node.Notes = append(node.Notes, "Photo bytes stay on this service (canonical)")
			}
		}
		if len(svc.CanonicalFor) > 0 {
			node.Notes = append(node.Notes,
				"Configured canonical for "+strings.Join(svc.CanonicalFor, ", "))
		}

		if !strings.EqualFold(svc.Action, "block") {
			edgeLabel := "OAuth proxy reads"
			style := FlowEdgeData
			if node.Status == FlowStatusNeedsAuth || node.Status == FlowStatusExpired {
				edgeLabel = "needs OAuth"
				style = FlowEdgeMeta
			}
			if !hasConnector {
				edgeLabel = "configured proxy"
			}
			edges = append(edges, FlowEdge{
				From:  "aegis",
				To:    node.ID,
				Label: edgeLabel,
				Style: style,
			})
		} else {
			edges = append(edges, FlowEdge{
				From:  "aegis",
				To:    node.ID,
				Label: "blocked",
				Style: FlowEdgeDenied,
			})
		}

		// Library fan-in edge for each kind this non-blocked service can provide.
		if !strings.EqualFold(svc.Action, "block") {
			for _, kindID := range node.Providers {
				if !h.Host.OwnerGrants().AllowsRead(kindID) {
					continue
				}
				kind := kinds.BuiltinKindByID(kindID)
				if kind == nil {
					continue
				}
				edges = append(edges, FlowEdge{
					From:  node.ID,
					To:    "aegis",
					Label: kind.Label + " → library",
					Style: FlowEdgeLibrary,
					Kind:  kindID,
				})
			}
		}

		nodes = append(nodes, node)
	}

	// Stable service order for the diagram.
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Kind != nodes[j].Kind {
			return flowKindOrder(nodes[i].Kind) < flowKindOrder(nodes[j].Kind)
		}
		return strings.ToLower(nodes[i].Label) < strings.ToLower(nodes[j].Label)
	})
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Label < edges[j].Label
	})

	kindList := h.KindCatalog.AllLibraryKinds()
	libraries := make([]FlowLibrary, 0, len(kindList))
	for _, kind := range kindList {
		providers := append([]string(nil), libraryProviders[kind.ID]...)
		// User-defined kinds are fed by schema sources, not OAuth connectors.
		if schema, ok := h.KindCatalog.UDKSchemaForKind(kind.ID); ok && len(providers) == 0 {
			for _, src := range schema.Sources {
				providers = append(providers, src.ID)
			}
		}
		sort.Strings(providers)
		libraries = append(libraries, FlowLibrary{
			ID:        kind.ID,
			Path:      kind.Path,
			Label:     kind.Label,
			SyncRule:  kind.SyncRule,
			Providers: providers,
		})
	}

	stats := h.buildFlowStats(window, windowLabel)
	grants := h.buildFlowGrants(stats)
	// Access arrows from each data grant to the library kinds its clients
	// used in the window (issue #425). Grant → service arrows are omitted so
	// the map keeps focus on kinds rather than backends.
	for _, g := range grants {
		for _, k := range g.Kinds {
			if k.Hits == 0 {
				continue
			}
			if !h.libraryKindExists(k.ID) {
				continue
			}
			edges = append(edges, FlowEdge{
				From:  "grant:" + g.ID,
				To:    "lib:" + k.ID,
				Label: accessEdgeLabel(k.Hits),
				Style: FlowEdgeData,
			})
		}
	}

	// Re-sort after agent nodes/edges were appended.
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Kind != nodes[j].Kind {
			return flowKindOrder(nodes[i].Kind) < flowKindOrder(nodes[j].Kind)
		}
		return strings.ToLower(nodes[i].Label) < strings.ToLower(nodes[j].Label)
	})
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Label < edges[j].Label
	})

	// Stamp recent hit counts onto nodes and library chips for the map UI.
	for i := range nodes {
		switch {
		case nodes[i].Kind == FlowKindAgent:
			nodes[i].Agents = stats.Agents.Distinct
			nodes[i].Hits = stats.TotalHits()
		case nodes[i].Kind == FlowKindService:
			id := strings.TrimPrefix(nodes[i].ID, "svc:")
			if st, ok := stats.Services[id]; ok {
				nodes[i].Hits = st.Hits
				nodes[i].Agents = st.Agents
			}
		}
	}
	for i := range libraries {
		if st, ok := stats.Libraries[libraries[i].ID]; ok {
			libraries[i].Hits = st.Hits
		}
	}

	// Map kinds are the core synced library kinds only (issue #32 cleanup).
	// Proxy-only connector surfaces stay on service nodes, not as chips.
	mapKinds := make([]FlowLibraryKind, 0, len(libraries))
	for _, lib := range libraries {
		mapKinds = append(mapKinds, FlowLibraryKind{
			ID:        lib.ID,
			Label:     lib.Label,
			Path:      lib.Path,
			Kind:      "library",
			Readable:  len(lib.Providers) > 0 && h.KindCatalog.LibraryReadableFor(lib.ID),
			Providers: lib.Providers,
		})
	}

	return FlowTopology{
		Runtime: FlowRuntime{
			AuthMode: h.Host.RuntimeFacts().AuthMode,
			BaseURL:  h.BaseURL,
		},
		Narrative: "Agents reach Aegis through Aperture’s hook and Aegis’s proxy/library APIs. " +
			"Aegis (nested inside Aperture) holds the token vault and response cache, attaches OAuth " +
			"tokens, reads your services, and merges library items into one neutral list. Photo bytes " +
			"stay on the source service.",
		Nodes:     nodes,
		Edges:     edges,
		Libraries: libraries,
		Kinds:     mapKinds,
		Grants:    grants,
		Stats:     stats,
	}
}

// buildFlowStats aggregates recent access metrics for the map (issue #32).
func (h *Handlers) buildFlowStats(window time.Duration, windowLabel string) FlowStats {
	if windowLabel == "" {
		windowLabel = "1h"
	}
	snap := h.MetricsSnapshot(window)
	services := make(map[string]FlowEndpointStats, len(snap.Services))
	for id, st := range snap.Services {
		services[id] = flowEndpointFromMetrics(st)
	}
	libraries := make(map[string]FlowEndpointStats, len(snap.Libraries))
	for id, st := range snap.Libraries {
		libraries[id] = flowEndpointFromMetrics(st)
	}
	recent := make([]FlowRecentAccess, 0, len(snap.Recent))
	for _, ev := range snap.Recent {
		recent = append(recent, FlowRecentAccess(ev))
	}
	return FlowStats{
		GeneratedAt: time.Now().UTC(),
		Window:      windowLabel,
		Windows:     append([]string(nil), FlowStatsWindows...),
		Agents: FlowAgentStats{
			Distinct: snap.Agents.Distinct,
			Sample:   snap.Agents.Sample,
			Detail:   flowAgentDetailsFromMetrics(snap.Agents.Detail),
		},
		Services:  services,
		Libraries: libraries,
		Recent:    recent,
	}
}

func flowAgentDetailsFromMetrics(detail []metrics.MetricsAgentDetail) []FlowAgentDetail {
	if len(detail) == 0 {
		return nil
	}
	out := make([]FlowAgentDetail, 0, len(detail))
	for _, d := range detail {
		out = append(out, FlowAgentDetail(d))
	}
	return out
}

func flowEndpointFromMetrics(st metrics.MetricsEndpointStats) FlowEndpointStats {
	return FlowEndpointStats(st)
}

// TotalHits sums service + library hits for the agents node badge.
func (fs FlowStats) TotalHits() int {
	total := 0
	for _, st := range fs.Services {
		total += st.Hits
	}
	for _, st := range fs.Libraries {
		total += st.Hits
	}
	return total
}

func flowKindOrder(kind string) int {
	switch kind {
	case FlowKindAgent:
		return 0
	case FlowKindGateway:
		return 1
	case FlowKindAegis:
		return 2
	case FlowKindStore:
		return 3
	case FlowKindToken:
		return 4
	default:
		return 5
	}
}

// accessEdgeLabel formats the hover label for agent → data access edges.
func accessEdgeLabel(hits int) string {
	if hits == 1 {
		return "1 hit"
	}
	return strconv.Itoa(hits) + " hits"
}

// buildFlowGrants lists the issued data grants joined with window usage
// (issue #425): what each holder grants ("what could be accessed"), which of
// those kinds its clients used ("what is being accessed"), and the caller
// identities behind them ("by who"). The join key is the access-metrics agent
// identity — "app:<id>" and "scope:<id>" name their holders directly, and
// every other identity (agent API key, tailnet logins) shares the agent data
// grants map. Callers with usage but no issued grant surface as orphans.
func (h *Handlers) buildFlowGrants(stats FlowStats) []FlowDataGrant {
	used := make(map[string]FlowAgentDetail, len(stats.Agents.Detail))
	for _, d := range stats.Agents.Detail {
		used[d.ID] = d
	}
	kindList, labels := h.flowGrantKindKeys()

	agent := FlowDataGrant{
		ID:      "agent",
		Kind:    FlowGrantAgent,
		Label:   "LLM agents",
		Detail:  "Owner-tier callers (agent API key and allowed tailnet logins) share one data grants map.",
		Clients: flowAgentClients(h.Host.RuntimeFacts().AllowedLogins, used),
	}
	flowFillGrant(&agent, kindList, labels, policy.AgentOverviewLevels(h.Host.DataGrants(), kindList), used)
	out := []FlowDataGrant{agent}

	holders := map[string]bool{agent.ID: true}
	if h.Apps != nil {
		appRows := h.Apps.List()
		sort.SliceStable(appRows, func(i, j int) bool {
			if appRows[i].Name != appRows[j].Name {
				return appRows[i].Name < appRows[j].Name
			}
			return appRows[i].ID < appRows[j].ID
		})
		for _, app := range appRows {
			id := "app:" + app.ID
			holders[id] = true
			levels, _ := policy.AppOverviewLevels(app.Scopes, kindList)
			g := FlowDataGrant{
				ID:       id,
				Kind:     FlowGrantApp,
				Label:    app.Name,
				Detail:   "Third-party application (" + app.ID + "). Scopes fail closed — unnamed types are off.",
				Disabled: app.Disabled,
				Clients:  []FlowGrantClient{{ID: id}},
			}
			flowFillGrant(&g, kindList, labels, levels, used)
			out = append(out, g)
		}
	}
	if h.Scopes != nil {
		scopeRows := h.Scopes.List()
		sort.SliceStable(scopeRows, func(i, j int) bool {
			if scopeRows[i].Name != scopeRows[j].Name {
				return scopeRows[i].Name < scopeRows[j].Name
			}
			return scopeRows[i].ID < scopeRows[j].ID
		})
		for _, sc := range scopeRows {
			id := "scope:" + sc.ID
			holders[id] = true
			rules := sc.Grants.Levels()
			levels := make(map[string]string, len(kindList))
			for _, key := range kindList {
				levels[key] = rules.Level(key)
			}
			label := sc.Name
			if label == "" {
				label = sc.ID
			}
			g := FlowDataGrant{
				ID:       id,
				Kind:     FlowGrantInvite,
				Label:    label,
				Detail:   "Invite token (external principal). Grants are the complete allow list — unnamed types are off.",
				Disabled: sc.Disabled,
				ReadOnly: sc.ReadOnly,
				Clients:  []FlowGrantClient{{ID: id}},
			}
			flowFillGrant(&g, kindList, labels, levels, used)
			out = append(out, g)
		}
	}

	// Usage without an issued grant (revoked or removed credential) stays
	// visible so "what is being accessed, and by who" cannot lose a caller.
	var orphans []FlowDataGrant
	for _, d := range stats.Agents.Detail {
		if holders[d.ID] || (!strings.HasPrefix(d.ID, "app:") && !strings.HasPrefix(d.ID, "scope:")) {
			continue
		}
		kind := FlowGrantApp
		detail := "Access recorded with no issued data grant (revoked or removed app)."
		if strings.HasPrefix(d.ID, "scope:") {
			kind = FlowGrantInvite
			detail = "Access recorded with no issued data grant (revoked or removed invite token)."
		}
		g := FlowDataGrant{
			ID:       d.ID,
			Kind:     kind,
			Label:    strings.TrimPrefix(strings.TrimPrefix(d.ID, "app:"), "scope:"),
			Detail:   detail,
			Disabled: true,
			Clients:  []FlowGrantClient{{ID: d.ID}},
		}
		flowFillGrant(&g, kindList, labels, nil, used)
		orphans = append(orphans, g)
	}
	sort.SliceStable(orphans, func(i, j int) bool {
		return orphans[i].ID < orphans[j].ID
	})
	out = append(out, orphans...)
	return out
}

// flowGrantKindKeys returns the kind keys every grant map resolves against
// (library perm types plus any extra keys on stored grants) and a display
// label per key.
func (h *Handlers) flowGrantKindKeys() ([]string, map[string]string) {
	preferred := make([]string, 0, 16)
	labels := make(map[string]string, 16)
	for _, kind := range h.KindCatalog.AllLibraryKinds() {
		key := kind.PermType
		if key == "" {
			key = kind.ID
		}
		preferred = append(preferred, key)
		labels[kind.ID] = kind.Label
		labels[key] = kind.Label
	}
	var extra []string
	extra = append(extra, policy.ExtraPolicyKeys(h.Host.DataGrants())...)
	if h.Apps != nil {
		for _, app := range h.Apps.List() {
			extra = append(extra, policy.ExtraPermissionKeys(app.Scopes)...)
		}
	}
	if h.Scopes != nil {
		for _, sc := range h.Scopes.All() {
			extra = append(extra, policy.ExtraPolicyKeys(sc.Grants)...)
		}
	}
	kindList := policy.OverviewKinds(preferred, extra)
	for _, key := range kindList {
		if _, ok := labels[key]; !ok {
			labels[key] = key
		}
	}
	return kindList, labels
}

// flowAgentClients lists the caller identities sharing the agent data grants
// map: the agent API key, the allowed tailnet logins, and any other non-app /
// non-scope identity seen in the window.
func flowAgentClients(allowedLogins []string, used map[string]FlowAgentDetail) []FlowGrantClient {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add("api-key")
	for _, login := range allowedLogins {
		add(login)
	}
	var extras []string
	for id := range used {
		if strings.HasPrefix(id, "app:") || strings.HasPrefix(id, "scope:") {
			continue
		}
		extras = append(extras, id)
	}
	sort.Strings(extras)
	for _, id := range extras {
		add(id)
	}
	clients := make([]FlowGrantClient, 0, len(ids))
	for _, id := range ids {
		clients = append(clients, FlowGrantClient{ID: id})
	}
	return clients
}

// flowFillGrant attaches window usage to a grant's clients and expands the
// granted kinds (level other than none) with per-kind usage.
func flowFillGrant(g *FlowDataGrant, kindList []string, labels map[string]string, levels map[string]string, used map[string]FlowAgentDetail) {
	for i := range g.Clients {
		d, ok := used[g.Clients[i].ID]
		if !ok {
			continue
		}
		g.Clients[i].Hits = d.Hits
		g.Clients[i].LastAt = d.LastAt
		g.Clients[i].Used = flowUsedKinds(d)
		g.Hits += d.Hits
		if d.LastAt.After(g.LastAt) {
			g.LastAt = d.LastAt
		}
	}
	for _, key := range kindList {
		level := shared.NormalizeAccess(levels[key])
		if level == "" || level == shared.AccessNone {
			continue
		}
		g.Kinds = append(g.Kinds, FlowGrantKind{
			ID:    key,
			Label: labels[key],
			Level: level,
			Hits:  flowGrantKindHits(g.Clients, used, key),
		})
	}
}

// flowGrantKindHits sums one kind's usage across the grant's clients. The
// per-kind counter covers both surfaces; the library counter fills in events
// recorded without a kind label.
func flowGrantKindHits(clients []FlowGrantClient, used map[string]FlowAgentDetail, key string) int {
	total := 0
	for _, c := range clients {
		d, ok := used[c.ID]
		if !ok {
			continue
		}
		if n := d.Kinds[key]; n > 0 {
			total += n
		} else {
			total += d.Libraries[key]
		}
	}
	return total
}

// flowUsedKinds lists the kinds a caller touched in the window.
func flowUsedKinds(d FlowAgentDetail) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for id := range d.Kinds {
		add(id)
	}
	for id := range d.Libraries {
		add(id)
	}
	sort.Strings(out)
	return out
}

func (h *Handlers) libraryKindExists(id string) bool {
	for _, k := range h.KindCatalog.AllLibraryKinds() {
		if k.ID == id {
			return true
		}
	}
	return false
}

// tokenDisplayName returns a human label for a vault connection id.
func (h *Handlers) TokenDisplayName(connID string) string {
	if name := h.Host.ConnectionName(connID); name != "" {
		return name
	}
	if svc, ok := h.Host.ServiceRow(connID); ok && svc.Name != "" {
		return svc.Name
	}
	// Fallback: title-case the id ("google" → "Google").
	if connID == "" {
		return "connection"
	}
	parts := strings.FieldsFunc(connID, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func flowServiceStatus(svc ServiceFacts, st tokens.TokenStatus) string {
	if st.HasToken {
		if st.Expired {
			return FlowStatusExpired
		}
		return FlowStatusConnected
	}
	if svc.StaticCredentialLabel != "" {
		return FlowStatusConnected
	}
	if svc.Connection == "" {
		return FlowStatusConfigured
	}
	return FlowStatusNeedsAuth
}

// flowProvidesKind reports whether the connector feeds the named library
// kind. Dispatch is registry-driven so a new kind cannot silently drop out
// of the flow map (issue #254).
func (h *Handlers) FlowProvidesKind(connector connectors.ServiceConnector, kindID string) bool {
	if k, ok := h.KindCatalog.RegistryKind(kindID); ok {
		return k.Provides(connector)
	}
	return false
}
