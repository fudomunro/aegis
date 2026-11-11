package library

import (
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

// canonicalServiceName returns the display name of the configured canonical
// service for kind, or "".
func (h *Handlers) CanonicalServiceName(kind string) string {
	for _, svc := range h.Host.Services() {
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
func MultiSourceMutationRule() string {
	return "Multi-source mutation: when an item has multiple sources[], write to the " +
		"canonical source first (sources[].canonical, or the configured home service). " +
		"Aegis does not auto-mirror writes to secondary services — ask the user before updating other copies. " +
		"To copy a playlist or liked tracks onto another service deliberately, use POST /v1/library/mirror " +
		"({\"kind\":\"playlists\"|\"tracks\", \"source_service\", \"source_id\", \"target_service\", \"dry_run\"})."
}

// writePolicyNotice returns system-instruction text for this service's write
// posture relative to configured canonical homes (issue #16 phase 2). Empty
// when the service cannot write library kinds or no home relationship exists.
func (h *Handlers) WritePolicyNotice(svc kinds.ServiceView) string {
	if strings.EqualFold(svc.Action, "block") {
		return ""
	}
	svcName := svc.Name
	if svcName == "" {
		svcName = svc.ID
	}
	var lines []string
	for _, kindID := range kinds.CanonicalKindIDs() {
		if !h.Host.OwnerGrants().AllowsWrite(kindID) {
			continue
		}
		homeName := h.CanonicalServiceName(kindID)
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

// WritePolicyNotices builds write-policy text for every matched inject
// service. Duplicate lines are dropped so multi-service prompts stay short.
func (h *Handlers) WritePolicyNotices(matched []kinds.ServiceView) []string {
	seen := map[string]bool{}
	var out []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		notice := h.WritePolicyNotice(svc)
		if notice == "" || seen[notice] {
			continue
		}
		seen[notice] = true
		out = append(out, notice)
	}
	return out
}
