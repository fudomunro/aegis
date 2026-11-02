package core

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
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
)

// mailRunnerServer wires a server with the mail archive, a mocked Gmail
// upstream, and the ingest runner registered on a real job store.
func mailRunnerServer(t *testing.T, upstream http.Handler) (*Server, *JobStore, *SyncWorker) {
	t.Helper()
	disableGmailArchivePace(t)
	var up *httptest.Server
	if upstream != nil {
		up = httptest.NewServer(upstream)
		t.Cleanup(up.Close)
	}
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.prom = NewPromMetrics()
	if up != nil {
		srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	}
	srv.tokens.Save(connectors.GoogleGmailServiceID, &OAuthToken{
		AccessToken: "access-token", Expiry: time.Now().Add(time.Hour),
	})
	store := testJobStore(t)
	srv.syncJobs = store
	reg := newSyncRunnerRegistry(srv)
	srv.syncWorker = NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1, Prom: srv.prom})
	return srv, store, srv.syncWorker
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
	st, err := srv.mail.Status()
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
	bad := &mailIngestRunner{srv: nil}
	if _, err := bad.Run(context.Background(), SyncJob{}); err == nil {
		t.Fatal("nil server must fail terminal")
	}

	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	runner = &mailIngestRunner{srv: srv}

	// Mail archive disabled.
	mail := srv.mail
	srv.mail = nil
	_, err := runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{}`)})
	srv.mail = mail
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
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Action = "block" })
	_, err = runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{}`)})
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Action = "inject" })
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
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })

	runner := &mailIngestRunner{srv: srv}
	step, err := runner.Run(context.Background(), SyncJob{
		Kind: mailIngestKind, JobType: SyncJobTypeIngest,
		Payload: json.RawMessage(`{"reset_cursor":true}`),
	})
	if err == nil {
		t.Fatal("quota must return an error")
	}
	if step.RetryAfter != quotaSyncBackoff {
		t.Fatalf("step.RetryAfter = %v, want %v", step.RetryAfter, quotaSyncBackoff)
	}
	var ra *SyncRetryAfter
	if !errors.As(err, &ra) || ra.After != quotaSyncBackoff {
		t.Fatalf("err = %v, want a SyncRetryAfter of %v", err, quotaSyncBackoff)
	}
	class, after := classifySyncError(err, step.RetryAfter)
	if class != SyncJobTypeErrorRetry || after != quotaSyncBackoff {
		t.Fatalf("classify = (%s,%v), want (retryable,%v)", class, after, quotaSyncBackoff)
	}
}

func TestMailIngestRunner_TakeoutStep(t *testing.T) {
	srv, store, w := mailRunnerServer(t, nil)

	// No takeout path configured is terminal.
	step, err := srv.runTakeoutStep(SyncJob{}, mailIngestPayload{Service: takeoutServiceID})
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("missing path err = %v, want terminal", err)
	}

	// An empty mbox file is a valid, completable import.
	mboxPath := t.TempDir() + "/empty.mbox"
	if err := os.WriteFile(mboxPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	mustEnqueue(t, store, SyncEnqueue{
		Kind: mailIngestKind, JobType: SyncJobTypeIngest,
		Service: takeoutServiceID,
		Payload: map[string]any{
			"service":      takeoutServiceID,
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
	if !strings.Contains(string(job.Progress), takeoutServiceID) {
		t.Fatalf("progress = %s, want the takeout service id", job.Progress)
	}
	_ = step
}

func TestMailArchiveSyncer_BuildsOnlyGmail(t *testing.T) {
	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	if srv.mailArchiveSyncer(connectors.GoogleGmailServiceID) == nil {
		t.Fatal("google-gmail must build a syncer")
	}
	if srv.mailArchiveSyncer("nope") != nil {
		t.Fatal("unknown service must not build a syncer")
	}
	// A connector that is not the Gmail adapter yields nil.
	srv.connectors["quickenish"] = NewQuickenConnector(srv, nil)
	if srv.mailArchiveSyncer("quickenish") != nil {
		t.Fatal("non-Gmail connector must not build a mail syncer")
	}
}

func TestSyncRunnerRegistry_MailIngestWired(t *testing.T) {
	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	reg := newSyncRunnerRegistry(srv)
	runner, ok := reg.For(mailIngestKind, SyncJobTypeIngest)
	if !ok {
		t.Fatal("mail ingest runner must be registered")
	}
	if runner.Kind() != "messages" {
		t.Fatalf("kind = %s, want messages", runner.Kind())
	}
	types := runner.JobTypes()
	if len(types) != 1 || types[0] != SyncJobTypeIngest {
		t.Fatalf("job types = %v, want [ingest]", types)
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
	st, err := srv.mail.Status()
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
