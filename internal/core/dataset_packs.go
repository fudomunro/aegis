package core

// Named demo dataset packs (issue #217). Each id is a data-scope partition
// with its own SampleLibrarySet + mail rows.

// demoDatasetIDs lists every named pack that `-seed-demo-datasets` writes.
func demoDatasetIDs() []string {
	return []string{LotRDatasetShire, LotRDatasetGondor, LincolnDataset}
}

// GenerateDemoDataset returns fixtures for a named pack (LotR, Lincoln, …).
// The second result is false when id is not a named pack — callers fall back
// to the generic namespaced sample set.
func GenerateDemoDataset(dataset string) (SampleLibrarySet, bool) {
	if set, ok := GenerateLotRDataset(dataset); ok {
		return set, true
	}
	return GenerateLincolnDataset(dataset)
}

// GenerateDemoMail returns archive rows for a named pack, or an empty set.
func GenerateDemoMail(dataset string) SampleMailSet {
	if set := GenerateLotRMail(dataset); len(set.Records) > 0 {
		return set
	}
	return GenerateLincolnMail(dataset)
}
