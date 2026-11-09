package shared

import (
	"net/http"
	"testing"
)

func TestCapabilityHelpers(t *testing.T) {
	caps := ServiceCapabilities{Kinds: []KindCapability{
		{Name: "tracks", Operations: []Operation{
			{Name: "list", Method: "GET"},
			{Name: "save", Method: "POST"},
		}},
		{Name: "playlists", Operations: []Operation{{Name: "list", Method: "GET"}}},
	}}
	if !KindKnown(caps, "tracks") || KindKnown(caps, "nope") {
		t.Fatal("KindKnown")
	}
	if KindHasWriteOp(caps, "playlists") || !KindHasWriteOp(caps, "tracks") {
		t.Fatal("KindHasWriteOp")
	}
	if KindHasAgentWriteOp(caps, "playlists") || !KindHasAgentWriteOp(caps, "tracks") {
		t.Fatal("KindHasAgentWriteOp")
	}
	if !IsMutatingMethod(http.MethodPost) || IsMutatingMethod(http.MethodGet) {
		t.Fatal("IsMutatingMethod")
	}
	writable := WritableKindsFromCapabilities(caps)
	if len(writable) != 1 || writable[0] != "tracks" {
		t.Fatalf("writable = %v", writable)
	}
}

func TestIsAgentDataWritePath(t *testing.T) {
	if IsAgentDataWritePath("/picker/sessions") || IsAgentDataWritePath("/search") {
		t.Fatal("picker/search are not agent data writes")
	}
	if !IsAgentDataWritePath("/me/tracks") {
		t.Fatal("library write path should count")
	}
}
