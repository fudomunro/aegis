package syncjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// mailRunnerServer wires the sync domain with the mail archive, a mocked
// Gmail upstream, and the ingest runner registered on a real job store.
func mailRunnerServer(t *testing.T, upstream http.Handler) (*Runners, *JobStore, *SyncWorker) {
	t.Helper()
	disableGmailArchivePace(t)
	var up *httptest.Server
	if upstream != nil {
		up = httptest.NewServer(upstream)
		t.Cleanup(up.Close)
	}
	a := openTestMailArchive(t)
	run := mailTestRunners(t, a)
	if up != nil {
		svc := run.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID]
		svc.Endpoint = up.URL
		run.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID] = svc
	}
	run.Host.(*fixtureHost).tokens[connectors.GoogleGmailServiceID] = "access-token"
	store := testJobStore(t)
	run.Jobs = store
	reg := NewRunnerRegistry()
	run.Register(reg)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	return run, store, w
}

// gmailListThenEmpty pages ids out in maxResults-sized batches and then
// returns an empty page, so a bounded ingest job chain reaches Done.
func gmailListThenEmpty(ids ...string) http.Handler {
	var mu sync.Mutex
	next := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users/me/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			w.Write([]byte(gmailPayloadJSON(id)))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		limit := 100
		if v := r.URL.Query().Get("maxResults"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}
		if limit <= 0 || next >= len(ids) {
			json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		end := next + limit
		if end > len(ids) {
			end = len(ids)
		}
		msgs := make([]map[string]string, 0, end-next)
		for _, id := range ids[next:end] {
			msgs = append(msgs, map[string]string{"id": id})
		}
		next = end
		body := map[string]any{"messages": msgs}
		if next < len(ids) {
			body["nextPageToken"] = fmt.Sprint(next)
		}
		json.NewEncoder(w).Encode(body)
	})
}

func TestMailIngestRunner_ChainsUntilDone(t *testing.T) {
	srv, store, w := mailRunnerServer(t, gmailListThenEmpty("m1", "m2"))
	mustEnqueue(t, store, SyncEnqueue{
		Kind: mailIngestKind, JobType: SyncJobTypeIngest,
		Service: connectors.GoogleGmailServiceID,
		Payload: map[string]any{"max_messages": 1, "reset_cursor": true},
	})

	// max_messages=1 forces one message per step: the chain must survive
	// several steps and only finish once the upstream list is empty.
	for i := 0; i < 6 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded (payload %s, progress %s)",
			job.State, job.Payload, job.Progress)
	}
	if !strings.Contains(string(job.Progress), `"stored":2`) {
		t.Fatalf("progress = %s, want two stored messages", job.Progress)
	}
	st, err := srv.Mail.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != 2 {
		t.Fatalf("archive rows = %d, want 2", st.MessageCount)
	}
}

func TestMailIngestRunner_TerminalConditions(t *testing.T) {
	runner := &mailIngestRunner{}

	// Unwired runner.
	if _, err := runner.Run(context.Background(), SyncJob{}); err == nil {
		t.Fatal("unwired runner must fail")
	} else if !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("err = %v, want terminal", err)
	}

	// Nil server.
	bad := &mailIngestRunner{run: nil}
	if _, err := bad.Run(context.Background(), SyncJob{}); err == nil {
		t.Fatal("nil server must fail terminal")
	}

	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	runner = &mailIngestRunner{run: srv}

	// Mail archive disabled.
	mail := srv.Mail
	srv.Mail = nil
	_, err := runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{}`)})
	srv.Mail = mail
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("disabled archive err = %v, want terminal", err)
	}

	// Bad payload.
	_, err = runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{`)})
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("bad payload err = %v, want terminal", err)
	}

	// No connector for the service.
	_, err = runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{"service":"nope"}`)})
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("unknown service err = %v, want terminal", err)
	}

	// Blocked service.
	gsvc := srv.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID]
	gsvc.Action = "block"
	srv.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID] = gsvc
	_, err = runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{}`)})
	gsvc.Action = "inject"
	srv.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID] = gsvc
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked service err = %v, want terminal blocked", err)
	}
}

func TestMailIngestRunner_QuotaReturnsRetryAfter(t *testing.T) {
	// Gmail 403 with a quota marker must surface as a retryable failure with
	// the quota-window backoff, not burn the attempt budget as terminal.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"Quota exceeded for quota metric"}}`))
	}))
	t.Cleanup(up.Close)
	srv, _, _ := mailRunnerServer(t, nil)
	gsvc := srv.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID]
	gsvc.Endpoint = up.URL
	srv.Host.(*fixtureHost).services[connectors.GoogleGmailServiceID] = gsvc

	runner := &mailIngestRunner{run: srv}
	step, err := runner.Run(context.Background(), SyncJob{
		Kind: mailIngestKind, JobType: SyncJobTypeIngest,
		Payload: json.RawMessage(`{"reset_cursor":true}`),
	})
	if err == nil {
		t.Fatal("quota must return an error")
	}
	if step.RetryAfter != connectors.GmailQuotaBackoff {
		t.Fatalf("step.RetryAfter = %v, want %v", step.RetryAfter, connectors.GmailQuotaBackoff)
	}
	var ra *SyncRetryAfter
	if !errors.As(err, &ra) || ra.After != connectors.GmailQuotaBackoff {
		t.Fatalf("err = %v, want a SyncRetryAfter of %v", err, connectors.GmailQuotaBackoff)
	}
	class, after := ClassifyError(err, 1, step.RetryAfter)
	if class != SyncErrorClassRetry || after != connectors.GmailQuotaBackoff {
		t.Fatalf("classify = (%s,%v), want (retryable,%v)", class, after, connectors.GmailQuotaBackoff)
	}
}

func TestMailIngestRunner_TakeoutStep(t *testing.T) {
	srv, store, w := mailRunnerServer(t, nil)

	// No takeout path configured is terminal.
	_, err := srv.DoMailIngest(context.Background(), connectors.TakeoutServiceID, shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("missing path err = %v, want terminal", err)
	}

	// An empty mbox file is a valid, completable import.
	mboxPath := t.TempDir() + "/empty.mbox"
	if err := os.WriteFile(mboxPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	// Takeout is a snapshot import: the import_snapshot job type defaults to
	// that source, so the payload need not name it.
	mustEnqueue(t, store, SyncEnqueue{
		Kind: mailIngestKind, JobType: SyncJobTypeImport,
		Service: connectors.TakeoutServiceID,
		Payload: map[string]any{
			"query":        "takeout:" + mboxPath,
			"max_messages": 10,
		},
	})
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim the takeout job")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("takeout job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	if !strings.Contains(string(job.Progress), connectors.TakeoutServiceID) {
		t.Fatalf("progress = %s, want the takeout service id", job.Progress)
	}
}

func TestSyncRunnerRegistry_MailIngestWired(t *testing.T) {
	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	reg := NewRunnerRegistry()
	srv.Register(reg)
	runner, ok := reg.For(mailIngestKind, SyncJobTypeIngest)
	if !ok {
		t.Fatal("mail ingest runner must be registered")
	}
	if runner.Kind() != "messages" {
		t.Fatalf("kind = %s, want messages", runner.Kind())
	}
	types := runner.JobTypes()
	if len(types) != 2 || types[0] != SyncJobTypeIngest || types[1] != SyncJobTypeImport {
		t.Fatalf("job types = %v, want [ingest import_snapshot]", types)
	}
	if _, ok := reg.For(mailIngestKind, SyncJobTypeRepair); ok {
		t.Fatal("repair is not registered in phase 1")
	}
}

func TestMailIngestRunner_EndToEndThroughQueue(t *testing.T) {
	srv, store, w := mailRunnerServer(t, gmailListThenEmpty("m1"))
	mustEnqueue(t, store, SyncEnqueue{
		Kind: mailIngestKind, JobType: SyncJobTypeIngest,
		Service: connectors.GoogleGmailServiceID,
		Payload: map[string]any{"reset_cursor": true},
	})
	w.Kick()
	for i := 0; i < 6 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded", job.State)
	}
	st, err := srv.Mail.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MessageCount != 1 {
		t.Fatalf("archive count = %d, want 1", st.MessageCount)
	}
	if w.LastSuccess()[mailIngestKind+"|"+connectors.GoogleGmailServiceID].IsZero() {
		t.Fatal("LastSuccess must record the mail ingest")
	}
}

// --- cleanup: one ingest path, lease renewal, import_snapshot ---

func TestParseMailIngestPayload_JobTypeDefaults(t *testing.T) {
	// import_snapshot defaults to the Takeout export, not Gmail.
	p, err := parseMailIngestPayload(SyncJob{JobType: SyncJobTypeImport})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != connectors.TakeoutServiceID {
		t.Fatalf("import_snapshot service = %q, want %q", p.Service, connectors.TakeoutServiceID)
	}
	p, err = parseMailIngestPayload(SyncJob{JobType: SyncJobTypeIngest})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != connectors.GoogleGmailServiceID {
		t.Fatalf("ingest service = %q, want google-gmail", p.Service)
	}
	// An explicit service always wins.
	p, err = parseMailIngestPayload(SyncJob{
		JobType: SyncJobTypeIngest, Service: "x",
		Payload: json.RawMessage(`{"service":"y"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != "y" {
		t.Fatalf("service = %q, want the payload's y", p.Service)
	}
	if _, err := parseMailIngestPayload(SyncJob{Payload: json.RawMessage(`{`)}); err == nil {
		t.Fatal("bad payload must fail terminal")
	}
}

func TestMailIngestRunner_ServesBothJobTypes(t *testing.T) {
	r := &mailIngestRunner{run: &Runners{}}
	got := r.JobTypes()
	if len(got) != 2 || got[0] != SyncJobTypeIngest || got[1] != SyncJobTypeImport {
		t.Fatalf("JobTypes = %v, want ingest and import_snapshot", got)
	}
	reg := NewRunnerRegistry()
	reg.Register(r)
	if _, ok := reg.For(mailIngestKind, SyncJobTypeIngest); !ok {
		t.Fatal("ingest must resolve")
	}
	if _, ok := reg.For(mailIngestKind, SyncJobTypeImport); !ok {
		t.Fatal("import_snapshot must resolve (it is no longer a dead type)")
	}
}
