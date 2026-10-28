package core

import (
	"strings"
	"testing"
)

func TestGenerateLincolnDataset_Shape(t *testing.T) {
	set, ok := GenerateLincolnDataset(LincolnDataset)
	if !ok {
		t.Fatal("lincoln pack missing")
	}
	if _, ok := GenerateLincolnDataset("shire"); ok {
		t.Fatal("lincoln generator must reject other ids")
	}
	names := contactsBlob(set)
	for _, want := range []string{"Abraham Lincoln", "Ulysses S. Grant", "Frederick Douglass"} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing contact %q", want)
		}
	}
	if len(set.Events) < 5 {
		t.Fatalf("events = %d, want a historical timeline", len(set.Events))
	}
	// Anchors: emancipation, Gettysburg, Appomattox news.
	if !strings.Contains(contactsBlob(SampleLibrarySet{Contacts: set.Contacts}), "Grant") {
		t.Fatal("grant missing")
	}
	found := map[string]bool{}
	for _, e := range set.Events {
		found[e.Title] = true
	}
	for _, title := range []string{"Emancipation Proclamation takes effect", "Gettysburg dedication remarks", "News from Appomattox Court House"} {
		if !found[title] {
			t.Fatalf("missing event %q", title)
		}
	}
}

func TestGenerateLincolnMail_ArchiveIsolation(t *testing.T) {
	srv := twoScopeServerWithLotR(t)
	for _, rec := range GenerateLincolnMail(LincolnDataset).Records {
		if err := srv.mail.Upsert(rec); err != nil {
			t.Fatalf("seed lincoln mail: %v", err)
		}
	}
	// Add a lincoln scope reusing the same store as shire/gondor.
	// twoScopeServer only has shire/gondor — query via dataset filter instead.
	shire := callMailSearch(t, srv, "YOUR_SHIRE_KEY")
	if strings.Contains(shire, "Appomattox") || strings.Contains(shire, "Gettysburg") {
		t.Fatalf("shire search leaked lincoln mail: %s", shire)
	}
}

func TestGenerateDemoDataset_Registry(t *testing.T) {
	for _, id := range demoDatasetIDs() {
		if _, ok := GenerateDemoDataset(id); !ok {
			t.Fatalf("registry missing pack %q", id)
		}
		if len(GenerateDemoMail(id).Records) == 0 {
			t.Fatalf("pack %q has no mail", id)
		}
	}
	if _, ok := GenerateDemoDataset("atlantis"); ok {
		t.Fatal("unknown pack must not generate")
	}
}
