package core

import "testing"

// TestRunCLIMode_Dispatch verifies the one-shot CLI flag routing (issue
// #274). When no CLI flag is set the server should start normally; when a
// flag is set the mode runs and Run exits.
func TestRunCLIMode_Dispatch(t *testing.T) {
	paths := DataPaths{Dir: t.TempDir()}

	// No CLI flags → continue normal startup.
	if _, handled := runCLIMode(cliModeOptions{}, paths); handled {
		t.Fatal("no CLI flags should not trigger a CLI mode")
	}

	// Whitespace-only import paths are ignored.
	if _, handled := runCLIMode(cliModeOptions{importQuicken: "  "}, paths); handled {
		t.Fatal("blank import-quicken should not trigger a CLI mode")
	}
	if _, handled := runCLIMode(cliModeOptions{importTimeline: " "}, paths); handled {
		t.Fatal("blank import-timeline should not trigger a CLI mode")
	}
	if _, handled := runCLIMode(cliModeOptions{importSignal: "\t"}, paths); handled {
		t.Fatal("blank import-signal should not trigger a CLI mode")
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

	// import-quicken triggers a Quicken import.
	if _, handled := runCLIMode(cliModeOptions{importQuicken: "/tmp/missing.qif"}, paths); !handled {
		t.Fatal("import-quicken should trigger a CLI mode")
	}

	// import-timeline triggers a Timeline import.
	if _, handled := runCLIMode(cliModeOptions{importTimeline: "/tmp/missing.json"}, paths); !handled {
		t.Fatal("import-timeline should trigger a CLI mode")
	}

	// import-signal triggers a Signal import.
	if _, handled := runCLIMode(cliModeOptions{importSignal: "/tmp/missing.jsonl"}, paths); !handled {
		t.Fatal("import-signal should trigger a CLI mode")
	}

	// import-takeout triggers a Takeout import.
	if _, handled := runCLIMode(cliModeOptions{importTakeout: "/tmp/missing.mbox"}, paths); !handled {
		t.Fatal("import-takeout should trigger a CLI mode")
	}
}
