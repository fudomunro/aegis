package shared

import (
	"net/http/httptest"
	"testing"
)

func TestServiceVisible(t *testing.T) {
	owner := (*DataScope)(nil)
	shire := &DataScope{Dataset: "shire"}
	gondor := &DataScope{Dataset: "gondor"}

	if !ServiceVisible("", owner) || !ServiceVisible("", shire) {
		t.Fatal("shared service must be visible to every scope")
	}
	if !ServiceVisible("shire", owner) {
		t.Fatal("owner scope sees every service")
	}
	if !ServiceVisible("shire", shire) {
		t.Fatal("matching dataset must be visible")
	}
	if ServiceVisible("shire", gondor) {
		t.Fatal("other dataset must be invisible")
	}
	if !ServiceVisible("", shire) {
		t.Fatal("empty dataset is shared")
	}
}

func TestDataScopeRequestView(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	if DataScopeFrom(r) != nil || Dataset(r) != "" {
		t.Fatal("no view attached is the owner principal")
	}
	if DataScopeFrom(WithDataScope(r, nil)) != nil {
		t.Fatal("nil view stays the owner principal")
	}

	scoped := WithDataScope(r, &DataScope{Dataset: "shire"})
	if got := DataScopeFrom(scoped); got == nil || got.Dataset != "shire" {
		t.Fatalf("DataScopeFrom = %+v, want shire", got)
	}
	if got := Dataset(scoped); got != "shire" {
		t.Fatalf("Dataset = %q, want shire", got)
	}
	// An empty-dataset view is a scoped principal in the default partition —
	// distinct from the owner view.
	def := WithDataScope(r, &DataScope{})
	if DataScopeFrom(def) == nil {
		t.Fatal("empty-dataset scope must not read as the owner principal")
	}
	if ServiceVisible("shire", DataScopeFrom(def)) {
		t.Fatal("default-partition scope must not see dataset-bound services")
	}
}
