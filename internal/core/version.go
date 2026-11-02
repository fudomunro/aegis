package core

// Build stamps for the running binary (issue #86). Overridden at link time:
//
//	go build -ldflags "-X github.com/fudomunro/aegis/internal/core.Version=v1.2.3 -X github.com/fudomunro/aegis/internal/core.Commit=abc123"
//
// Every maintenance signal and the aegis_build_info metric carry these so a
// failure can be attributed to the code that produced it.
var (
	Version = "dev"
	Commit  = "none"
)
