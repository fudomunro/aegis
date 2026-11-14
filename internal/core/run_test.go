package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

// TestRunCLIMode_Dispatch verifies the one-shot CLI flag routing (issue
// #274). When no CLI flag is set the server should start normally; when a
// flag is set the mode runs and Run exits. Local import store flags are
// covered generically over the connectors registry (issue #393) — a new
// store is tested here without edits.
func TestRunCLIMode_Dispatch(t *testing.T) {
	paths := DataPaths{Dir: t.TempDir()}

	// No CLI flags → continue normal startup.
	if _, handled := runCLIMode(cliModeOptions{}, paths); handled {
		t.Fatal("no CLI flags should not trigger a CLI mode")
	}

	// Whitespace-only import paths are ignored (every declared store).
	for _, spec := range connectors.LocalImportStoreSpecs() {
		if _, handled := runCLIMode(cliModeOptions{importStores: map[string]string{spec.Stem: "  "}}, paths); handled {
			t.Fatalf("blank %s should not trigger a CLI mode", spec.ImportFlagName())
		}
	}
	if _, handled := runCLIMode(cliModeOptions{importTakeout: ""}, paths); handled {
		t.Fatal("blank import-takeout should not trigger a CLI mode")
	}

	// seed-sample triggers seeding and exits (code may be non-zero without
	// a mail archive; the dispatch is what matters here).
	if _, handled := runCLIMode(cliModeOptions{seedSample: true, seedSampleCount: 2}, paths); !handled {
		t.Fatal("seed-sample should trigger a CLI mode")
	}

	// seed-demo-datasets triggers demo seeding.
	if _, handled := runCLIMode(cliModeOptions{seedDemoDatasets: true}, paths); !handled {
		t.Fatal("seed-demo-datasets should trigger a CLI mode")
	}

	// seed-lotr is an alias for seed-demo-datasets.
	if _, handled := runCLIMode(cliModeOptions{seedLotR: true}, paths); !handled {
		t.Fatal("seed-lotr should trigger a CLI mode")
	}

	// token-new-pass triggers rekey.
	if _, handled := runCLIMode(cliModeOptions{tokenNewPass: "new", tokenPass: "old"}, paths); !handled {
		t.Fatal("token-new-pass should trigger a CLI mode")
	}

	// Every declared store's import flag triggers its import mode.
	for _, spec := range connectors.LocalImportStoreSpecs() {
		if _, handled := runCLIMode(cliModeOptions{importStores: map[string]string{spec.Stem: "/tmp/missing-export"}}, paths); !handled {
			t.Fatalf("%s should trigger a CLI mode", spec.ImportFlagName())
		}
	}

	// import-takeout triggers a Takeout import.
	if _, handled := runCLIMode(cliModeOptions{importTakeout: "/tmp/missing.mbox"}, paths); !handled {
		t.Fatal("import-takeout should trigger a CLI mode")
	}
}
