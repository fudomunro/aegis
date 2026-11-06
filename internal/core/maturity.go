package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// Connection maturity catalog (issue #247): the per-connection declaration
// of what each service offers in theory and what Aegis serves through the
// connection. The dashboard grades it into the maturity mark; nothing here
// is runtime data, so both the release-published build and the instance
// build read the same coverage statement.
//
// Connector-owned entries live in internal/connectors/maturity.go beside the
// connectors they describe. The entries below cover the core-local import
// stores. maturity_test.go holds the aggregate honest against the
// connectors themselves: every callable capability and every implemented
// library feed must be marked supported, and no entry may claim support for
// a type the connector does not recognize at all. Field coverage is graded
// against types.MaturityContentFields.
//
// Add a data type row when a service offers a recognizable kind of thing
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
func ConnectionMaturityCatalog() []types.ConnectionMaturity {
	return connectors.ConnectionMaturityCatalog()
}

// MaturityByService indexes the catalog by connector ID.
func MaturityByService() map[string]types.ConnectionMaturity {
	cat := ConnectionMaturityCatalog()
	out := make(map[string]types.ConnectionMaturity, len(cat))
	for _, m := range cat {
		out[m.Service] = m
	}
	return out
}
