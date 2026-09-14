package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Flow topology node kinds.
const (
	FlowKindAgent    = "agent"
	FlowKindGateway  = "gateway"
	FlowKindGuardian = "guardian"
	FlowKindStore    = "store"
	FlowKindService  = "service"
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
	// Kind is agent, gateway, guardian, store, or service.
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Status applies mainly to services (connected, needs-auth, …).
	Status    string   `json:"status,omitempty"`
	Action    string   `json:"action,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	DataTypes []string `json:"data_types,omitempty"`
	// Providers lists unified library kinds this service can feed.
	Providers []string `json:"providers,omitempty"`
	// Notes are short caveats (e.g. photo bytes stay upstream).
	Notes []string `json:"notes,omitempty"`
}

// FlowEdge is a directed connection between two nodes.
type FlowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	// Style is hook, access, data, meta, denied, or library.
	Style string `json:"style"`
}

// FlowLibrary describes one unified library endpoint and its fan-in sources.
type FlowLibrary struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Label     string   `json:"label"`
	SyncRule  string   `json:"sync_rule"`
	Providers []string `json:"providers"`
}

// FlowRuntime is the non-secret runtime context for the map.
type FlowRuntime struct {
	AuthMode string `json:"auth_mode"`
	BaseURL  string `json:"base_url"`
}

// FlowTopology is the admin data-flow map payload (issue #25).
type FlowTopology struct {
	Runtime   FlowRuntime   `json:"runtime"`
	Narrative string        `json:"narrative"`
	Nodes     []FlowNode    `json:"nodes"`
	Edges     []FlowEdge    `json:"edges"`
	Libraries []FlowLibrary `json:"libraries"`
}

// HandleAdminFlow returns the data-flow topology for the admin Map view.
// GET /v1/admin/flow
func (s *Server) HandleAdminFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	topo := s.buildFlowTopology()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(topo)
}

// buildFlowTopology assembles the graph from config, connectors, and tokens.
// Secrets are never included.
func (s *Server) buildFlowTopology() FlowTopology {
	tokens := s.tokens.Status()

	nodes := []FlowNode{
		{
			ID:     "agents",
			Kind:   FlowKindAgent,
			Label:  "LLM Agents",
			Detail: "Tailnet (or API-key) clients that talk to Guardian",
		},
		{
			ID:     "aperture",
			Kind:   FlowKindGateway,
			Label:  "Aperture",
			Detail: "Pre-request hook injects Guardian proxy URLs into agent prompts",
		},
		{
			ID:     "guardian",
			Kind:   FlowKindGuardian,
			Label:  "Guardian",
			Detail: "Hook · service proxy · unified library · permissions",
		},
		{
			ID:     "tokens",
			Kind:   FlowKindStore,
			Label:  "Token vault",
			Detail: "Encrypted OAuth tokens on disk",
		},
		{
			ID:     "cache",
			Kind:   FlowKindStore,
			Label:  "Response cache",
			Detail: "In-memory GET cache for proxied and library responses",
		},
	}

	edges := []FlowEdge{
		{From: "agents", To: "aperture", Label: "LLM traffic", Style: FlowEdgeHook},
		{From: "aperture", To: "guardian", Label: "POST /v1/pre-request", Style: FlowEdgeHook},
		{From: "agents", To: "guardian", Label: "proxy + library API", Style: FlowEdgeAccess},
		{From: "guardian", To: "tokens", Label: "OAuth tokens", Style: FlowEdgeMeta},
		{From: "guardian", To: "cache", Label: "cached GETs", Style: FlowEdgeMeta},
	}

	// Library kinds this server can aggregate, with which services feed them.
	libraryProviders := make(map[string][]string)
	for _, kind := range libraryKinds {
		libraryProviders[kind.ID] = nil
	}

	for _, svc := range s.config.Services {
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
			node.Status = flowServiceStatus(svc, tokens[svc.ID])
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
				From:  "guardian",
				To:    node.ID,
				Label: edgeLabel,
				Style: style,
			})
		} else {
			edges = append(edges, FlowEdge{
				From:  "guardian",
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
					To:    "guardian",
					Label: kind.Label + " → library",
					Style: FlowEdgeLibrary,
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

	return FlowTopology{
		Runtime: FlowRuntime{
			AuthMode: s.authMode,
			BaseURL:  s.baseURL,
		},
		Narrative: "Agents ask Guardian (via Aperture’s hook and Guardian’s proxy/library APIs). " +
			"Guardian attaches OAuth tokens, reads your services, caches responses, and merges " +
			"library items into one neutral list. Photo bytes stay on the source service.",
		Nodes:     nodes,
		Edges:     edges,
		Libraries: libraries,
	}
}

func flowKindOrder(kind string) int {
	switch kind {
	case FlowKindAgent:
		return 0
	case FlowKindGateway:
		return 1
	case FlowKindGuardian:
		return 2
	case FlowKindStore:
		return 3
	default:
		return 4
	}
}

func flowServiceStatus(svc Service, st TokenStatus) string {
	if svc.OAuth == nil {
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
	default:
		return false
	}
}
