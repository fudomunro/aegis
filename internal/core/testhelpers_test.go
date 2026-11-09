package core

// Test-only helpers, moved out of production sources so they stop shipping
// in the binary. They were unreachable from any non-test call site.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// AccessEventToAudit projects a redacted access event onto the durable
// audit row (issue #105).
func AccessEventToAudit(ev AccessEvent) audit.Event {
	return audit.Event{
		At:       ev.At,
		Agent:    ev.Agent,
		Surface:  ev.Surface,
		Target:   ev.Target,
		Kind:     ev.Kind,
		Decision: ev.Decision,
		Reason:   ev.Reason,
		Cache:    ev.Cache,
		Method:   ev.Method,
		Status:   ev.Status,
		ClientIP: ev.ClientIP,
	}
}

// canonicalServiceIDs returns the set of non-blocked service IDs declared as
// the authoritative location for kind.
func (c *Config) canonicalServiceIDs(kind string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]bool{}
	for _, svc := range c.Services {
		if svc.IsCanonicalFor(kind) {
			out[svc.ID] = true
		}
	}
	return out
}

// updateService applies mut to a service under the write lock. Tests use it
// to install mock endpoints.
func (c *Config) updateService(id string, mut func(*Service)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc := c.serviceByID(id)
	if svc == nil {
		return false
	}
	mut(svc)
	return true
}

// newMessage builds a single-source neutral message.
func newMessage(service, serviceName, id, channel, subject, snippet, sentAt string) types.Message {
	src := types.MessageSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return types.Message{
		Channel: channel,
		Subject: subject,
		Snippet: snippet,
		SentAt:  sentAt,
		Sources: []types.MessageSource{src},
	}
}

// PhotosTakenDuring returns photos whose taken_at falls inside the visit or
// trip window (issue #142). This is the photos/places join demo: sample
// fixtures pair visits with photos captured during them.
func PhotosTakenDuring(photos []types.Photo, v types.PlaceVisit) []types.Photo {
	start := types.NormalizePlaceVisitTime(v.StartAt)
	end := types.NormalizePlaceVisitTime(v.EndAt)
	if end == "" {
		end = start
	}
	if start == "" {
		return nil
	}
	out := make([]types.Photo, 0, 8)
	for _, p := range photos {
		taken := types.NormalizePlaceVisitTime(p.TakenAt)
		if taken == "" {
			continue
		}
		if taken < start {
			continue
		}
		// Open-ended visit (no end_at): any photo at or after start.
		if v.EndAt != "" && end != "" && taken > end {
			continue
		}
		out = append(out, p)
	}
	return out
}

// GenerateSampleMailFor builds a sample mail set tagged with a data-scope
// dataset so archive queries can partition it (docs/data-scopes.md). Row ids
// are namespaced so two datasets can share one archive without colliding on
// UNIQUE(service, service_msg_id).
func GenerateSampleMailFor(dataset string, n int) SampleMailSet {
	set := GenerateSampleMail(n)
	ds := strings.TrimSpace(dataset)
	if ds == "" {
		return set
	}
	suffix := " (" + ds + ")"
	for i := range set.Records {
		set.Records[i].Dataset = ds
		set.Records[i].ID = ds + ":" + set.Records[i].ID
		set.Records[i].Subject += suffix
		set.Records[i].FromName += suffix
	}
	return set
}

// listUserDefined fetches every source for one schema and returns the raw
// pre-merge rows (the aggregate pipeline owns merge + encode).
func listUserDefined(ctx context.Context, schema types.UserDefinedTypeSchema, baseDir string, client *http.Client) (items []types.UserDefinedItem, truncated bool, err error) {
	// Preserve source declaration order for stable source stamps.
	srcs := schema.Sources
	if len(srcs) == 0 {
		return nil, false, fmt.Errorf("no sources configured")
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	errs := make([]error, 0, len(srcs))
	all := make([]types.UserDefinedItem, 0, len(srcs)*8)
	for _, src := range srcs {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A bad source must not fail the whole list (fail-open).
			items, trunc, ferr := fetchUserDefinedSource(ctx, schema, src, baseDir, client)
			mu.Lock()
			defer mu.Unlock()
			if ferr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", src.ID, ferr))
				return
			}
			if trunc {
				truncated = true
			}
			all = append(all, items...)
		}()
	}
	wg.Wait()
	// Fail-open: return partial rows even when some sources erred. Only
	// return an error when every source failed and nothing was collected.
	if len(all) == 0 && len(errs) > 0 {
		return nil, truncated, errors.Join(errs...)
	}
	return all, truncated, nil
}

// newTestTokenStore opens a token store with a tiny PBKDF2 cost so the suite
// stays fast. Production uses tokens.NewTokenStore.
func newTestTokenStore(path, passphrase string) (*tokens.TokenStore, error) {
	return tokens.NewStoreWithIterations(path, passphrase, 1_000)
}

// mustSecretStore returns a TokenStore suitable for InitServiceConnectionsStore
// tests. Use a real path when the test reloads secrets from disk (issue #184).
func mustSecretStore(t *testing.T, path string) *tokens.TokenStore {
	t.Helper()
	ts, err := newTestTokenStore(path, "test-pass")
	if err != nil {
		t.Fatalf("newTestTokenStore(%q): %v", path, err)
	}
	return ts
}

// writeJSONFile writes v as indented JSON to path (test fixtures).
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestSearchIndex opens an in-temp-dir search index for Server tests.
func newTestSearchIndex(t *testing.T) *search.Index {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "search-index.db"))
	if err != nil {
		t.Fatalf("search.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// openTestMailArchive opens a mail archive in a temp dir for Server tests.
func openTestMailArchive(t *testing.T) *mail.Archive {
	t.Helper()
	a, err := mail.Open(filepath.Join(t.TempDir(), "mail-archive.db"))
	if err != nil {
		t.Fatalf("mail.Open: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// sampleMailRecord builds a minimal mail.Record fixture for Server tests.
func sampleMailRecord(id, subject, body string) mail.Record {
	return mail.Record{
		ID:       id,
		Service:  "google-gmail",
		Subject:  subject,
		BodyText: body,
		SentAt:   "2024-06-01T09:00:00Z",
	}
}

// mustCopyLedger opens a copy ledger in a temp dir for Server tests.
func mustCopyLedger(t *testing.T) *copies.Ledger {
	t.Helper()
	l, err := copies.Open(filepath.Join(t.TempDir(), "copy-ledger.db"))
	if err != nil {
		t.Fatalf("copies.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// writeDataScopes writes a data_scopes.json fixture and returns its path.
func writeDataScopes(t *testing.T, list []scopes.Scope) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 2, "data_scopes": list})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "data_scopes.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// forEachPlaylistSource and forEachTrackSource project sources for generic
// stamping of the kinds already moved into the kinds package (issue #312) —
// their production projections live beside those kinds and are unexported.
func forEachPlaylistSource(p *types.Playlist, f func(kinds.SourceMut)) {
	for i := range p.Sources {
		src := &p.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}

func forEachTrackSource(t *types.Track, f func(kinds.SourceMut)) {
	for i := range t.Sources {
		src := &t.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}

// forEachContactSource and forEachPhotoSource project sources for generic
// stamping of the kinds already moved into the kinds package (issue #312) —
// their production projections live beside those kinds and are unexported.
func forEachContactSource(item *types.Contact, f func(kinds.SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}

func forEachPhotoSource(item *types.Photo, f func(kinds.SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(kinds.SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
