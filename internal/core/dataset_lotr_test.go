package core

import (
	"strings"
	"testing"
)

func TestGenerateLotRDataset_DistinctPartitions(t *testing.T) {
	shire, ok := GenerateLotRDataset(LotRDatasetShire)
	if !ok {
		t.Fatal("shire pack missing")
	}
	gondor, ok := GenerateLotRDataset(LotRDatasetGondor)
	if !ok {
		t.Fatal("gondor pack missing")
	}
	if _, ok := GenerateLotRDataset("narnia"); ok {
		t.Fatal("unknown dataset must not generate")
	}

	shireBlob := contactsBlob(shire)
	gondorBlob := contactsBlob(gondor)
	if strings.Contains(shireBlob, "Boromir") {
		t.Fatal("shire pack contains gondor contacts")
	}
	if strings.Contains(gondorBlob, "Samwise") {
		t.Fatal("gondor pack contains shire contacts")
	}
	if len(shire.Events) == 0 || len(gondor.Events) == 0 {
		t.Fatal("both packs need events")
	}
	if len(shire.Places) == 0 || len(gondor.Places) == 0 {
		t.Fatal("both packs need places for trip queries")
	}
}

func TestGenerateLotRMail_PartitionedRows(t *testing.T) {
	shire := GenerateLotRMail(LotRDatasetShire)
	gondor := GenerateLotRMail(LotRDatasetGondor)
	if len(shire.Records) == 0 || len(gondor.Records) == 0 {
		t.Fatal("both packs need mail")
	}
	for _, rec := range shire.Records {
		if rec.Dataset != LotRDatasetShire {
			t.Fatalf("shire row dataset = %q", rec.Dataset)
		}
		if !strings.HasPrefix(rec.ID, LotRDatasetShire+":") {
			t.Fatalf("shire row id not namespaced: %q", rec.ID)
		}
	}
	for _, rec := range gondor.Records {
		if rec.Dataset != LotRDatasetGondor {
			t.Fatalf("gondor row dataset = %q", rec.Dataset)
		}
	}
	// Query-interest anchors: a dated journey and a war dispatch.
	if !mailHasSubject(shire, "Leave as soon as you can") {
		t.Fatal("shire pack missing Gandalf departure mail")
	}
	if !mailHasSubject(gondor, "Dawn after the siege") {
		t.Fatal("gondor pack missing Helm's Deep dispatch")
	}
}

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

func contactsBlob(set SampleLibrarySet) string {
	var b strings.Builder
	for _, c := range set.Contacts {
		b.WriteString(c.Name)
		b.WriteByte('\n')
	}
	return b.String()
}

func mailHasSubject(set SampleMailSet, subject string) bool {
	for _, rec := range set.Records {
		if rec.Subject == subject {
			return true
		}
	}
	return false
}

// twoScopeServerWithLotR is twoScopeServer with LotR packs instead of generic
// sample fixtures, and LotR mail in the archive.
func twoScopeServerWithLotR(t *testing.T) *Server {
	t.Helper()
	srv := twoScopeServer(t)
	shireLib, _ := GenerateLotRDataset(LotRDatasetShire)
	gondorLib, _ := GenerateLotRDataset(LotRDatasetGondor)
	srv.connectors["sample-library-shire"] = NewSampleLibraryConnector("sample-library-shire", LotRDatasetShire, shireLib)
	srv.connectors["sample-library-gondor"] = NewSampleLibraryConnector("sample-library-gondor", LotRDatasetGondor, gondorLib)
	for _, rec := range GenerateLotRMail(LotRDatasetShire).Records {
		if err := srv.mail.Upsert(rec); err != nil {
			t.Fatalf("seed lotr shire mail: %v", err)
		}
	}
	for _, rec := range GenerateLotRMail(LotRDatasetGondor).Records {
		if err := srv.mail.Upsert(rec); err != nil {
			t.Fatalf("seed lotr gondor mail: %v", err)
		}
	}
	return srv
}
