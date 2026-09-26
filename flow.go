package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// FlowKindAgentInstance is one named agent (issue #114). The aggregate
	// "agents" node remains FlowKindAgent for the gateway cluster.
	FlowKindAgentInstance = "agent-instance"
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
	Status    string   `json:"status,omitempty"`
	Action    string   `json:"action,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	DataTypes []string `json:"data_types,omitempty"`
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

// FlowDataType is one synced library data type shown on the admin map.
// Map chips are limited to library kinds Aegis merges; proxy-only
// connector surfaces are not listed here.
type FlowDataType struct {
	ID string `json:"id"`
	// Label is the human name ("liked tracks").
	Label string `json:"label"`
	// Path is the library endpoint.
	Path string `json:"path,omitempty"`
	// Kind is "library" (unified endpoint). Kept for API stability.
	Kind string `json:"kind"`
	// Readable is true when at least one non-blocked service allows reads.
	Readable bool `json:"readable"`
	// Providers lists non-blocked services that can supply this type.
	Providers []string `json:"providers,omitempty"`
}

// FlowRuntime is the non-secret runtime context for the map.
type FlowRuntime struct {
	AuthMode string `json:"auth_mode"`
	BaseURL  string `json:"base_url"`
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
	// DataTypes maps recorded data type → hits.
	DataTypes map[string]int `json:"data_types,omitempty"`
	LastAt    time.Time      `json:"last_at,omitempty"`
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
	DataTypes map[string]int `json:"data_types,omitempty"`
}

// FlowRecentAccess is a redacted sample of recent agent access (no bodies).
type FlowRecentAccess struct {
	At       time.Time `json:"at"`
	Agent    string    `json:"agent"`
	Surface  string    `json:"surface"`
	Target   string    `json:"target"`
	DataType string    `json:"data_type,omitempty"`
	Cache    string    `json:"cache,omitempty"`
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
	Runtime   FlowRuntime    `json:"runtime"`
	Narrative string         `json:"narrative"`
	Nodes     []FlowNode     `json:"nodes"`
	Edges     []FlowEdge     `json:"edges"`
	Libraries []FlowLibrary  `json:"libraries"`
	DataTypes []FlowDataType `json:"data_types"`
	// Stats is recent agent access for the requested window (issue #32).
	Stats FlowStats `json:"stats"`
}

// HandleAdminFlow returns the data-flow topology for the admin Map view.
// GET /v1/admin/flow[?window=5m|1h|24h]
func (s *Server) HandleAdminFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	window, windowLabel := parseFlowWindow(r.URL.Query().Get("window"))
	topo := s.buildFlowTopology(window, windowLabel)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(topo)
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
func (s *Server) buildFlowTopology(window time.Duration, windowLabel string) FlowTopology {
	tokens := s.tokens.Status()

	nodes := []FlowNode{
		{
			ID:     "agents",
			Kind:   FlowKindAgent,
			Label:  "LLM Agents",
			Detail: "Tailnet (or API-key) clients that talk to Aegis",
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
			Detail: "In-memory cache of synced library data types and proxied GETs",
			Parent: "aegis",
		},
	}

	// List tokens currently held in the vault (connection id → status).
	// Labels use connection/service display names; secrets never appear.
	tokenIDs := make([]string, 0, len(tokens))
	for id := range tokens {
		tokenIDs = append(tokenIDs, id)
	}
	sort.Strings(tokenIDs)
	for _, connID := range tokenIDs {
		st := tokens[connID]
		status := FlowStatusConnected
		if st.Expired {
			status = FlowStatusExpired
		}
		nodes = append(nodes, FlowNode{
			ID:     "tok:" + connID,
			Kind:   FlowKindToken,
			Label:  s.tokenDisplayName(connID),
			Detail: "OAuth token held for this connection",
			Parent: "tokens",
			Status: status,
		})
	}

	edges := []FlowEdge{
		{From: "agents", To: "aperture", Label: "LLM traffic", Style: FlowEdgeHook},
		{From: "aperture", To: "aegis", Label: "POST /v1/pre-request", Style: FlowEdgeHook},
		{From: "agents", To: "aegis", Label: "proxy + library API", Style: FlowEdgeAccess},
	}

	// Library kinds this server can aggregate, with which services feed them.
	libraryProviders := make(map[string][]string)
	for _, kind := range libraryKinds {
		libraryProviders[kind.ID] = nil
	}

	for _, svc := range s.config.ServiceSnapshots() {
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
			connID := s.config.connectionIDForService(svc.ID)
			node.ConnectionID = connID
			node.Status = flowServiceStatus(svc, tokens[connID])
		}

		connector, hasConnector := s.connectors[svc.ID]
		if hasConnector {
			caps := connector.Capabilities()
			types := make([]string, 0, len(caps.DataTypes))
			for _, dt := range caps.DataTypes {
				types = append(types, dt.Name)
			}
			node.DataTypes = types

			for _, kind := range libraryKinds {
				if flowProvidesKind(connector, kind.ID) {
					node.Providers = append(node.Providers, kind.ID)
					if !strings.EqualFold(svc.Action, "block") {
						libraryProviders[kind.ID] = append(libraryProviders[kind.ID], svc.ID)
					}
				}
			}
			if _, ok := connector.(PhotoProvider); ok {
				node.Notes = append(node.Notes, "Photo bytes stay on this service (canonical)")
			}
		}
		if len(svc.CanonicalFor) > 0 {
			node.Notes = append(node.Notes,
				"Configured canonical for "+strings.Join(svc.CanonicalFor, ", ")+" (issue #16)")
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
				if !svc.Permissions.AllowsRead(kindID) {
					continue
				}
				kind := LibraryKindByID(kindID)
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

	libraries := make([]FlowLibrary, 0, len(libraryKinds))
	for _, kind := range libraryKinds {
		providers := append([]string(nil), libraryProviders[kind.ID]...)
		sort.Strings(providers)
		libraries = append(libraries, FlowLibrary{
			ID:        kind.ID,
			Path:      kind.Path,
			Label:     kind.Label,
			SyncRule:  kind.SyncRule,
			Providers: providers,
		})
	}

	stats := s.buildFlowStats(window, windowLabel)
	// Individual agent nodes + access arrows to library data types they used
	// (issue #114). Agent → service arrows are omitted so the map keeps focus
	// on data types rather than backends.
	for _, ad := range stats.Agents.Detail {
		nodes = append(nodes, FlowNode{
			ID:     "agent:" + ad.ID,
			Kind:   FlowKindAgentInstance,
			Label:  ad.ID,
			Detail: "Active in the selected access window",
			Parent: "agents",
			Hits:   ad.Hits,
			Agents: 1,
		})
		for libID, hits := range ad.Libraries {
			edges = append(edges, FlowEdge{
				From:  "agent:" + ad.ID,
				To:    "lib:" + libID,
				Label: accessEdgeLabel(hits),
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

	// Map data types are the core synced library kinds only (issue #32 cleanup).
	// Proxy-only connector surfaces stay on service nodes, not as chips.
	dataTypes := make([]FlowDataType, 0, len(libraries))
	for _, lib := range libraries {
		dataTypes = append(dataTypes, FlowDataType{
			ID:        lib.ID,
			Label:     lib.Label,
			Path:      lib.Path,
			Kind:      "library",
			Readable:  len(lib.Providers) > 0 && s.libraryReadableFor(lib.ID),
			Providers: lib.Providers,
		})
	}

	return FlowTopology{
		Runtime: FlowRuntime{
			AuthMode: s.authMode,
			BaseURL:  s.baseURL,
		},
		Narrative: "Agents reach Aegis through Aperture’s hook and Aegis’s proxy/library APIs. " +
			"Aegis (nested inside Aperture) holds the token vault and response cache, attaches OAuth " +
			"tokens, reads your services, and merges library items into one neutral list. Photo bytes " +
			"stay on the source service.",
		Nodes:     nodes,
		Edges:     edges,
		Libraries: libraries,
		DataTypes: dataTypes,
		Stats:     stats,
	}
}

// buildFlowStats aggregates recent access metrics for the map (issue #32).
func (s *Server) buildFlowStats(window time.Duration, windowLabel string) FlowStats {
	if windowLabel == "" {
		windowLabel = "1h"
	}
	snap := s.metricsSnapshot(window)
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
		recent = append(recent, FlowRecentAccess{
			At:       ev.At,
			Agent:    ev.Agent,
			Surface:  ev.Surface,
			Target:   ev.Target,
			DataType: ev.DataType,
			Cache:    ev.Cache,
		})
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

func flowAgentDetailsFromMetrics(detail []MetricsAgentDetail) []FlowAgentDetail {
	if len(detail) == 0 {
		return nil
	}
	out := make([]FlowAgentDetail, 0, len(detail))
	for _, d := range detail {
		out = append(out, FlowAgentDetail{
			ID:        d.ID,
			Hits:      d.Hits,
			Services:  d.Services,
			Libraries: d.Libraries,
			DataTypes: d.DataTypes,
			LastAt:    d.LastAt,
		})
	}
	return out
}

func flowEndpointFromMetrics(st MetricsEndpointStats) FlowEndpointStats {
	return FlowEndpointStats{
		Hits:      st.Hits,
		Agents:    st.Agents,
		CacheHits: st.CacheHits,
		CacheMiss: st.CacheMiss,
		DataTypes: st.DataTypes,
	}
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
	case FlowKindAgentInstance:
		return 1
	case FlowKindGateway:
		return 2
	case FlowKindAegis:
		return 3
	case FlowKindStore:
		return 4
	case FlowKindToken:
		return 5
	default:
		return 6
	}
}

// accessEdgeLabel formats the hover label for agent → data access edges.
func accessEdgeLabel(hits int) string {
	if hits == 1 {
		return "1 hit"
	}
	return strconv.Itoa(hits) + " hits"
}

// tokenDisplayName returns a human label for a vault connection id.
func (s *Server) tokenDisplayName(connID string) string {
	if s.config != nil {
		if conn, ok := s.config.Connections[connID]; ok && conn.Name != "" {
			return conn.Name
		}
		if svc := s.config.GetService(connID); svc != nil && svc.Name != "" {
			return svc.Name
		}
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

func flowServiceStatus(svc Service, st TokenStatus) string {
	if svc.Connection == "" {
		return FlowStatusConfigured
	}
	if !st.HasToken {
		return FlowStatusNeedsAuth
	}
	if st.Expired {
		return FlowStatusExpired
	}
	return FlowStatusConnected
}

func flowProvidesKind(connector ServiceConnector, kindID string) bool {
	switch kindID {
	case "playlists":
		_, ok := connector.(PlaylistProvider)
		return ok
	case "tracks":
		_, ok := connector.(LikedTrackProvider)
		return ok
	case "albums":
		_, ok := connector.(AlbumProvider)
		return ok
	case "artists":
		_, ok := connector.(ArtistProvider)
		return ok
	case "contacts":
		_, ok := connector.(ContactProvider)
		return ok
	case "photos":
		_, ok := connector.(PhotoProvider)
		return ok
	case "events":
		_, ok := connector.(CalendarEventProvider)
		return ok
	case "messages":
		_, ok := connector.(MessageProvider)
		return ok
	default:
		return false
	}
}
