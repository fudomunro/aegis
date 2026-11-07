package shared

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandUserPath resolves a leading "~" to the current user's home directory.
// A path without a home prefix is returned trimmed but otherwise unchanged.
func ExpandUserPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
