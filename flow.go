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
	// Parent nests this node inside another (e.g. guardian inside aperture).
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

// FlowDataType is one data type agents can reach through this instance.
type FlowDataType struct {
	ID string `json:"id"`
	// Label is the human name ("liked tracks").
	Label string `json:"label"`
	// Path is the library endpoint when Kind is library.
	Path string `json:"path,omitempty"`
	// Kind is "library" (unified endpoint) or "proxy" (service proxy only).
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

// FlowTopology is the admin data-flow map payload (issue #25).
type FlowTopology struct {
	Runtime   FlowRuntime    `json:"runtime"`
	Narrative string         `json:"narrative"`
	Nodes     []FlowNode     `json:"nodes"`
	Edges     []FlowEdge     `json:"edges"`
	Libraries []FlowLibrary  `json:"libraries"`
	DataTypes []FlowDataType `json:"data_types"`
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
			Detail: "Gateway shell — pre-request hook injects Guardian URLs into agent prompts",
		},
		{
			ID:     "guardian",
			Kind:   FlowKindGuardian,
			Label:  "Guardian",
			Detail: "Hook · service proxy · unified library · permissions",
			Parent: "aperture",
		},
		{
			ID:     "tokens",
			Kind:   FlowKindStore,
			Label:  "Token vault",
			Detail: "Encrypted OAuth tokens on disk",
			Parent: "guardian",
		},
		{
			ID:     "cache",
			Kind:   FlowKindStore,
			Label:  "Response cache",
			Detail: "In-memory GET cache for proxied and library responses",
			Parent: "guardian",
		},
	}

	edges := []FlowEdge{
		{From: "agents", To: "aperture", Label: "LLM traffic", Style: FlowEdgeHook},
		{From: "aperture", To: "guardian", Label: "POST /v1/pre-request", Style: FlowEdgeHook},
		{From: "agents", To: "guardian", Label: "proxy + library API", Style: FlowEdgeAccess},
	}

	// Library kinds this server can aggregate, with which services feed them.
	libraryProviders := make(map[string][]string)
	for _, kind := range libraryKinds {
		libraryProviders[kind.ID] = nil
	}

	// Proxy-only data types seen on non-blocked connectors (beyond library kinds).
	proxyTypes := map[string]map[string]bool{} // type → service id set

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
			libraryIDs := map[string]bool{}
			for _, kind := range libraryKinds {
				libraryIDs[kind.ID] = true
			}
			for _, dt := range caps.DataTypes {
				types = append(types, dt.Name)
				if strings.EqualFold(svc.Action, "block") || libraryIDs[dt.Name] {
					continue
				}
				if proxyTypes[dt.Name] == nil {
					proxyTypes[dt.Name] = map[string]bool{}
				}
				proxyTypes[dt.Name][svc.ID] = true
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

	dataTypes := make([]FlowDataType, 0, len(libraryKinds)+len(proxyTypes))
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
	proxyIDs := make([]string, 0, len(proxyTypes))
	for id := range proxyTypes {
		proxyIDs = append(proxyIDs, id)
	}
	sort.Strings(proxyIDs)
	for _, id := range proxyIDs {
		providers := make([]string, 0, len(proxyTypes[id]))
		for svcID := range proxyTypes[id] {
			providers = append(providers, svcID)
		}
		sort.Strings(providers)
		dataTypes = append(dataTypes, FlowDataType{
			ID:        id,
			Label:     id,
			Kind:      "proxy",
			Readable:  true,
			Providers: providers,
		})
	}

	return FlowTopology{
		Runtime: FlowRuntime{
			AuthMode: s.authMode,
			BaseURL:  s.baseURL,
		},
		Narrative: "Agents reach Guardian through Aperture’s hook and Guardian’s proxy/library APIs. " +
			"Guardian (nested inside Aperture) holds the token vault and response cache, attaches OAuth " +
			"tokens, reads your services, and merges library items into one neutral list. Photo bytes " +
			"stay on the source service.",
		Nodes:     nodes,
		Edges:     edges,
		Libraries: libraries,
		DataTypes: dataTypes,
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
