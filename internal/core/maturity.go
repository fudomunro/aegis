package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
)

// Connection maturity catalog (issue #247): the per-connection declaration
// of what each service offers in theory and what Aegis serves through the
// connection. The dashboard tool grades it into the maturity mark; nothing
// here is runtime data, so every build reads the same coverage statement.
// Ownership evidence reads the same catalog.
//
// Connector-owned entries live in internal/connectors/maturity.go beside the
// connectors they describe. The entries below cover the core-local import
// stores. maturity_test.go holds the aggregate honest against the
// connectors themselves: every callable capability and every implemented
// library feed must be marked supported, and no entry may claim support for
// a kind the connector does not recognize at all. Field coverage is graded
// against connectors.MaturityContentFields.
//
// Add a kind row when a service offers a recognizable kind of thing
// Aegis does not serve — shows, audiobooks, photo albums, drafts, posts,
// budgets — because the unsupported gap is the point of the catalog. Do not
// add rows for concepts a supported type already carries as populated
// fields (Paperless tags and correspondents live on Document, so the
// documents row grades them). A connection whose only offered type is
// fully served and fully filled does read as "complete"; the per-type table
// on the page is what shows how narrow that is.

// ConnectionMaturityCatalog returns the maturity declaration for every
// real connection. Sample fixtures and the drift-injection harness are
// excluded: they are test surfaces, not connections to mature.
// ConnectionMaturityCatalog returns the maturity declaration for every
// real connection. Sample fixtures and the drift-injection harness are
// excluded: they are test surfaces, not connections to mature.
func ConnectionMaturityCatalog() []connectors.ConnectionMaturity {
	return connectors.ConnectionMaturityCatalog()
}

// MaturityByService indexes the catalog by connector ID.
func MaturityByService() map[string]connectors.ConnectionMaturity {
	cat := ConnectionMaturityCatalog()
	out := make(map[string]connectors.ConnectionMaturity, len(cat))
	for _, m := range cat {
		out[m.Service] = m
	}
	return out
}
