package core

import (
	"github.com/fudomunro/aegis/internal/kinds"
)

import (
	"fmt"
	"strings"
)

// canonicalKindIDs returns kinds allowed in a service's canonical_for list
// (issue #183: derived from kinds.BuiltinLibraryKinds CanonicalAllowed).
func canonicalKindIDs() []string {
	return kinds.CanonicalKindIDs()
}

func isCanonicalKindID(id string) bool {
	return kinds.IsCanonicalKindID(id)
}

// IsCanonicalFor reports whether this service is the configured authoritative
// location for the given library kind (issue #16). Blocked services never count.
func (s Service) IsCanonicalFor(kind string) bool {
	if strings.EqualFold(s.Action, "block") {
		return false
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return false
	}
	for _, k := range s.CanonicalFor {
		if strings.EqualFold(strings.TrimSpace(k), kind) {
			return true
		}
	}
	return false
}

// normalizeCanonicalFor trims entries and drops empties / duplicates.
func normalizeCanonicalFor(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		k := strings.TrimSpace(raw)
		if k == "" || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateCanonicalFor checks kind IDs and that at most one non-blocked service
// claims each kind.
func validateCanonicalFor(services []Service) error {
	owner := map[string]string{} // kind -> service id
	for _, svc := range services {
		for _, raw := range svc.CanonicalFor {
			k := strings.TrimSpace(raw)
			if k == "" {
				continue
			}
			if !isCanonicalKindID(k) {
				return fmt.Errorf("service %q: unknown canonical_for kind %q", svc.ID, k)
			}
			if strings.EqualFold(svc.Action, "block") {
				continue
			}
			lower := strings.ToLower(k)
			if prev, ok := owner[lower]; ok && prev != svc.ID {
				return fmt.Errorf("canonical_for %q claimed by both %q and %q", k, prev, svc.ID)
			}
			owner[lower] = svc.ID
		}
	}
	return nil
}

// canonicalServiceName returns the display name of the configured canonical
// service for kind, or "".
func (s *Server) canonicalServiceName(kind string) string {
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.IsCanonicalFor(kind) {
			if svc.Name != "" {
				return svc.Name
			}
			return svc.ID
		}
	}
	return ""
}

// multiSourceMutationRule is injected whenever library instructions are used
// so agents hit the home copy first and do not invent auto-mirroring.
func multiSourceMutationRule() string {
	return "Multi-source mutation: when an item has multiple sources[], write to the " +
		"canonical source first (sources[].canonical, or the configured home service). " +
		"Aegis does not auto-mirror writes to secondary services — ask the user before updating other copies. " +
		"To copy a playlist or liked tracks onto another service deliberately, use POST /v1/library/mirror " +
		"({\"kind\":\"playlists\"|\"tracks\", \"source_service\", \"source_id\", \"target_service\", \"dry_run\"})."
}

// writePolicyNotice returns system-instruction text for this service's write
// posture relative to configured canonical homes (issue #16 phase 2). Empty
// when the service cannot write library kinds or no home relationship exists.
func (s *Server) writePolicyNotice(svc Service) string {
	if strings.EqualFold(svc.Action, "block") {
		return ""
	}
	svcName := svc.Name
	if svcName == "" {
		svcName = svc.ID
	}
	var lines []string
	for _, kindID := range canonicalKindIDs() {
		if !s.effectivePermissions(&svc).AllowsWrite(kindID) {
			continue
		}
		homeName := s.canonicalServiceName(kindID)
		label := kindID
		if k := LibraryKindByID(kindID); k != nil {
			label = k.Label
		}
		switch {
		case svc.IsCanonicalFor(kindID):
			lines = append(lines, fmt.Sprintf(
				"%s is the configured canonical home for %s — send mutations of %s here first.",
				svcName, label, label,
			))
		case homeName != "":
			lines = append(lines, fmt.Sprintf(
				"For %s mutations, prefer %s (canonical home) before %s. Secondary copies are not auto-synced.",
				label, homeName, svcName,
			))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Write policy:\n" + strings.Join(lines, "\n")
}

// writePolicyNotices builds write-policy text for every matched inject service.
// Duplicate lines are dropped so multi-service prompts stay short.
func (s *Server) writePolicyNotices(matched []Service) []string {
	seen := map[string]bool{}
	var out []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		notice := s.writePolicyNotice(svc)
		if notice == "" || seen[notice] {
			continue
		}
		seen[notice] = true
		out = append(out, notice)
	}
	return out
}
