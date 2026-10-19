package types

// MarkOptInDataType records a data-type key that stays hidden until the
// permissions map / data grant names it explicitly (places pattern, #142).
// User-defined kinds register here so a new UDT is never agent-visible by
// default (issue #15).
func MarkOptInDataType(dataType string) {
	if dataType == "" {
		return
	}
	optInDefaultNone[dataType] = true
}

// UnmarkOptInDataType removes an opt-in mark. Intended for tests and for
// unloading a user-defined kind at runtime.
func UnmarkOptInDataType(dataType string) {
	delete(optInDefaultNone, dataType)
}

// IsOptInDataType reports whether the data-type key requires explicit enable.
func IsOptInDataType(dataType string) bool {
	return optInDefaultNone[dataType]
}
