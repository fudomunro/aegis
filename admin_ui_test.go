package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issue #69 UI cleanup markers: no top-bar stats, Advanced tab consolidates
// cache/runtime/map details, setup wizard removed, Services help when empty.
func TestAdminHTML_UICleanup(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		`id="tab-map"`,
		`id="tab-services"`,
		`id="tab-library"`,
		`id="tab-advanced"`,
		`id="panel-advanced"`,
		`id="services-help"`,
		`id="map-libraries"`,
		`id="map-recent"`,
		`id="cache-body"`,
		`id="runtime-body"`,
		"Connect your services",
		"renderServicesHelp",
		"FLOW_TABS",
		`"advanced"`,
		"map-svg-wrap",
		// Services card hierarchy (polish): name + data flow + connection first
		"svc-card",
		"svc-title-row",
		"svc-name",
		"svc-flow",
		"svc-connection",
		"svc-details",
		"svc-body",
		"serviceConnectionInline",
		// Photo picker shares the title-line connection cluster
		"supports_photo_picker",
		"Pick photos",
		// Advanced toggle under the permissions form (flow + details)
		"svc-advanced",
		"data-service-advanced",
		"openAdvanced",
		"<summary>Advanced</summary>",
		"serviceFlowRows",
		"serviceConnectionStatus",
		"serviceLibraryKinds",
		"data-service-flow",
		"data-service-status",
		"Agents → Guardian → service",
		"has_event_provider",
		"has_message_provider",
		// Connect/Reconnect only when not connected (issue #69 polish)
		"!status.connected",
		`"Reconnect"`,
		// Shared instructions above filter; form stays on each card
		`id="agent-data-access-help"`,
		"Changes save immediately",
		"never add capabilities",
		"renderPermEditor(svc)",
		"perm-live",
		"applyPermissionsLocally",
		// Data-type icons on permission form labels
		"dataTypeIconHTML",
		"perm-type-icon",
		"data-type-icon",
		// Mail archive sync controls on Advanced (local compressed mailbox)
		`id="mail-archive-summary"`,
		`id="mail-archive-body"`,
		`id="sync-mail-archive"`,
		`id="sync-mail-archive-restart"`,
		`id="reload-mail-archive"`,
		"/v1/admin/mail/status",
		"/v1/admin/mail/sync",
		"runMailArchiveSync",
		"MAIL_SYNC_MAX_BATCHES",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing UI cleanup marker %q", want)
		}
	}

	for _, gone := range []string{
		`id="status-strip"`,
		"renderStatusStrip",
		`id="tab-cache"`,
		`id="tab-runtime"`,
		`id="panel-cache"`,
		`id="panel-runtime"`,
		"Setup wizard",
		"/v1/setup",
		"Guardian Admin",
		// Instructions are not repeated on every card form
		"<h4>Agent data access</h4>",
		"Choose what agents can use on this service",
	} {
		if strings.Contains(body, gone) {
			t.Fatalf("admin HTML still contains removed piece %q", gone)
		}
	}
}

func TestHandleSetup_Removed(t *testing.T) {
	// The setup wizard route is gone; /v1/setup must not be a documented admin path.
	// Route wiring lives in main(); assert the HTML/markup no longer advertises it.
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if strings.Contains(w.Body.String(), "/v1/setup") {
		t.Fatal("admin HTML must not reference removed /v1/setup wizard")
	}
}
