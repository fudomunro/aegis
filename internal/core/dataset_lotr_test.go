package core

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/sampledata"
)

func TestLotRMail_DatasetIsolationInArchive(t *testing.T) {
	srv := twoScopeServerWithLotR(t)
	shire := callMailSearch(t, srv, "YOUR_SHIRE_KEY")
	gondor := callMailSearch(t, srv, "YOUR_GONDOR_KEY")
	if strings.Contains(shire, "Pelennor") || strings.Contains(shire, "boromir@") {
		t.Fatalf("shire search leaked gondor: %s", shire)
	}
	if strings.Contains(gondor, "Bag End") && strings.Contains(gondor, "Samwise") {
		t.Fatalf("gondor search leaked shire mail: %s", gondor)
	}
	if !strings.Contains(shire, "Bag End") && !strings.Contains(shire, "Prancing Pony") {
		t.Fatalf("shire search missing journey mail: %s", shire)
	}
}

func TestLotRLibrary_DatasetIsolation(t *testing.T) {
	srv := twoScopeServerWithLotR(t)
	shire := callLibrary(t, srv, "YOUR_SHIRE_KEY", "/v1/library/contacts")
	gondor := callLibrary(t, srv, "YOUR_GONDOR_KEY", "/v1/library/contacts")
	if strings.Contains(shire, "Boromir") {
		t.Fatalf("shire contacts leaked gondor: %s", shire)
	}
	if strings.Contains(gondor, "Samwise") {
		t.Fatalf("gondor contacts leaked shire: %s", gondor)
	}
	if !strings.Contains(shire, "Frodo") || !strings.Contains(gondor, "Aragorn") {
		t.Fatal("packs missing their own principals")
	}
}

func twoScopeServerWithLotR(t *testing.T) *Server {
	t.Helper()
	srv := twoScopeServer(t)
	shireLib, _ := sampledata.GenerateLotRDataset(sampledata.LotRDatasetShire)
	gondorLib, _ := sampledata.GenerateLotRDataset(sampledata.LotRDatasetGondor)
	srv.connectors["sample-library-shire"] = sampledata.NewSampleLibraryConnector("sample-library-shire", sampledata.LotRDatasetShire, shireLib)
	srv.connectors["sample-library-gondor"] = sampledata.NewSampleLibraryConnector("sample-library-gondor", sampledata.LotRDatasetGondor, gondorLib)
	for _, rec := range sampledata.GenerateLotRMail(sampledata.LotRDatasetShire).Records {
		if err := srv.mail.Upsert(rec); err != nil {
			t.Fatalf("seed lotr shire mail: %v", err)
		}
	}
	for _, rec := range sampledata.GenerateLotRMail(sampledata.LotRDatasetGondor).Records {
		if err := srv.mail.Upsert(rec); err != nil {
			t.Fatalf("seed lotr gondor mail: %v", err)
		}
	}
	return srv
}
