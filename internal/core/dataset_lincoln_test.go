package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/sampledata"
)

func TestGenerateLincolnMail_ArchiveIsolation(t *testing.T) {
	srv := twoScopeServerWithLotR(t)
	for _, rec := range sampledata.GenerateLincolnMail(sampledata.LincolnDataset).Records {
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
