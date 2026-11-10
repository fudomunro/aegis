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
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/core/syncjobs"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

// mailRunnerServer wires a server with the mail archive, a mocked Gmail
// upstream, and the ingest runner registered on a real job store.
func mailRunnerServer(t *testing.T, upstream http.Handler) (*Server, *syncjobs.JobStore, *syncjobs.SyncWorker) {
	t.Helper()
	disableGmailArchivePace(t)
	var up *httptest.Server
	if upstream != nil {
		up = httptest.NewServer(upstream)
		t.Cleanup(up.Close)
	}
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.prom = metrics.NewPromMetrics("", "")
	if up != nil {
		srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	}
	srv.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{
		AccessToken: "access-token", Expiry: time.Now().Add(time.Hour),
	})
	store := testJobStore(t)
	srv.syncJobs = store
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = syncjobs.NewSyncWorker(store, srv.syncRunners, syncjobs.SyncWorkerOptions{Workers: 1, Prom: srv.prom})
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
	mustEnqueue(t, store, syncjobs.SyncEnqueue{
		Kind: mailIngestKind, JobType: syncjobs.SyncJobTypeIngest,
		Service: connectors.GoogleGmailServiceID,
		Payload: map[string]any{"max_messages": 1, "reset_cursor": true},
	})

	// max_messages=1 forces one message per step: the chain must survive
	// several steps and only finish once the upstream list is empty.
	for i := 0; i < 6 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(syncjobs.SyncFilter{})[0]
	if job.State != syncjobs.SyncJobSucceeded {
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
	if _, err := runner.Run(context.Background(), syncjobs.SyncJob{}); err == nil {
		t.Fatal("unwired runner must fail")
	} else if !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("err = %v, want terminal", err)
	}

	// Nil server.
	bad := &mailIngestRunner{srv: nil}
	if _, err := bad.Run(context.Background(), syncjobs.SyncJob{}); err == nil {
		t.Fatal("nil server must fail terminal")
	}

	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	runner = &mailIngestRunner{srv: srv}

	// Mail archive disabled.
	mail := srv.mail
	srv.mail = nil
	_, err := runner.Run(context.Background(), syncjobs.SyncJob{Payload: json.RawMessage(`{}`)})
	srv.mail = mail
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("disabled archive err = %v, want terminal", err)
	}

	// Bad payload.
	_, err = runner.Run(context.Background(), syncjobs.SyncJob{Payload: json.RawMessage(`{`)})
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("bad payload err = %v, want terminal", err)
	}

	// No connector for the service.
	_, err = runner.Run(context.Background(), syncjobs.SyncJob{Payload: json.RawMessage(`{"service":"nope"}`)})
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("unknown service err = %v, want terminal", err)
	}

	// Blocked service.
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Action = "block" })
	_, err = runner.Run(context.Background(), syncjobs.SyncJob{Payload: json.RawMessage(`{}`)})
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
	step, err := runner.Run(context.Background(), syncjobs.SyncJob{
		Kind: mailIngestKind, JobType: syncjobs.SyncJobTypeIngest,
		Payload: json.RawMessage(`{"reset_cursor":true}`),
	})
	if err == nil {
		t.Fatal("quota must return an error")
	}
	if step.RetryAfter != connectors.GmailQuotaBackoff {
		t.Fatalf("step.RetryAfter = %v, want %v", step.RetryAfter, connectors.GmailQuotaBackoff)
	}
	var ra *syncjobs.SyncRetryAfter
	if !errors.As(err, &ra) || ra.After != connectors.GmailQuotaBackoff {
		t.Fatalf("err = %v, want a syncjobs.SyncRetryAfter of %v", err, connectors.GmailQuotaBackoff)
	}
	class, after := syncjobs.ClassifyError(err, 1, step.RetryAfter)
	if class != syncjobs.SyncErrorClassRetry || after != connectors.GmailQuotaBackoff {
		t.Fatalf("classify = (%s,%v), want (retryable,%v)", class, after, connectors.GmailQuotaBackoff)
	}
}

func TestMailIngestRunner_TakeoutStep(t *testing.T) {
	srv, store, w := mailRunnerServer(t, nil)

	// No takeout path configured is terminal.
	_, err := srv.doMailIngest(context.Background(), mailIngestPayload{Service: connectors.TakeoutServiceID})
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("missing path err = %v, want terminal", err)
	}

	// An empty mbox file is a valid, completable import.
	mboxPath := t.TempDir() + "/empty.mbox"
	if err := os.WriteFile(mboxPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	// Takeout is a snapshot import: the import_snapshot job type defaults to
	// that source, so the payload need not name it.
	mustEnqueue(t, store, syncjobs.SyncEnqueue{
		Kind: mailIngestKind, JobType: syncjobs.SyncJobTypeImport,
		Service: connectors.TakeoutServiceID,
		Payload: map[string]any{
			"query":        "takeout:" + mboxPath,
			"max_messages": 10,
		},
	})
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim the takeout job")
	}
	job := store.List(syncjobs.SyncFilter{})[0]
	if job.State != syncjobs.SyncJobSucceeded {
		t.Fatalf("takeout job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	if !strings.Contains(string(job.Progress), connectors.TakeoutServiceID) {
		t.Fatalf("progress = %s, want the takeout service id", job.Progress)
	}
}

func TestMailIngestFor_Wired(t *testing.T) {
	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	if srv.mailIngestFor(connectors.GoogleGmailServiceID) == nil {
		t.Fatal("google-gmail must build an ingest")
	}
	if srv.mailIngestFor(connectors.TakeoutServiceID) == nil {
		t.Fatal("takeout must build an ingest")
	}
	if srv.mailIngestFor("nope") != nil {
		t.Fatal("unknown service must not build an ingest")
	}
	// A connector that does not implement MailIngest yields nil.
	srv.connectors["quickenish"] = connectors.NewQuickenConnector(nil)
	srv.wireMailIngests()
	if srv.mailIngestFor("quickenish") != nil {
		t.Fatal("non-Gmail connector must not build a mail ingest")
	}
}

func TestSyncRunnerRegistry_MailIngestWired(t *testing.T) {
	srv, _, _ := mailRunnerServer(t, gmailListThenEmpty("m1"))
	reg := newSyncRunnerRegistry(srv)
	runner, ok := reg.For(mailIngestKind, syncjobs.SyncJobTypeIngest)
	if !ok {
		t.Fatal("mail ingest runner must be registered")
	}
	if runner.Kind() != "messages" {
		t.Fatalf("kind = %s, want messages", runner.Kind())
	}
	types := runner.JobTypes()
	if len(types) != 2 || types[0] != syncjobs.SyncJobTypeIngest || types[1] != syncjobs.SyncJobTypeImport {
		t.Fatalf("job types = %v, want [ingest import_snapshot]", types)
	}
	if _, ok := reg.For(mailIngestKind, syncjobs.SyncJobTypeRepair); ok {
		t.Fatal("repair is not registered in phase 1")
	}
}

func TestMailIngestRunner_EndToEndThroughQueue(t *testing.T) {
	srv, store, w := mailRunnerServer(t, gmailListThenEmpty("m1"))
	mustEnqueue(t, store, syncjobs.SyncEnqueue{
		Kind: mailIngestKind, JobType: syncjobs.SyncJobTypeIngest,
		Service: connectors.GoogleGmailServiceID,
		Payload: map[string]any{"reset_cursor": true},
	})
	w.Kick()
	for i := 0; i < 6 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(syncjobs.SyncFilter{})[0]
	if job.State != syncjobs.SyncJobSucceeded {
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

// --- cleanup: one ingest path, lease renewal, import_snapshot ---

// The HTTP route and the queue runner must report the same bounded result for
// the same input. Before the cleanup the route called SyncMailArchive
// directly, so the two could drift.
func TestMailSyncRouteAndRunnerShareOnePath(t *testing.T) {
	disableGmailArchivePace(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users/me/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			w.Write([]byte(gmailPayloadJSON(id)))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "m1"}, {"id": "m2"}},
		})
	}))
	t.Cleanup(up.Close)

	// Route first.
	a1 := openTestMailArchive(t)
	srv1 := mailTestServer(t, a1)
	srv1.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv1.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	w := doAdminMailReq(t, srv1, http.MethodPost, "/v1/admin/mail/sync", `{"max_messages":5,"reset_cursor":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("route status = %d body=%s", w.Code, w.Body.String())
	}
	var routeResult shared.MailIngestResult
	if err := json.Unmarshal(w.Body.Bytes(), &routeResult); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Runner second, fresh archive, same request.
	a2 := openTestMailArchive(t)
	srv2 := mailTestServer(t, a2)
	srv2.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv2.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	runnerResult, err := srv2.doMailIngest(context.Background(), mailIngestPayload{
		Service: connectors.GoogleGmailServiceID, MaxMessages: 5, ResetCursor: true,
	})
	if err != nil {
		t.Fatalf("doMailIngest: %v", err)
	}

	for _, field := range []struct {
		name          string
		route, runner any
	}{
		{"stored", routeResult.Stored, runnerResult.Stored},
		{"listed", routeResult.Listed, runnerResult.Listed},
		{"failed", routeResult.Failed, runnerResult.Failed},
		{"skipped", routeResult.Skipped, runnerResult.Skipped},
		{"done", routeResult.Done, runnerResult.Done},
		{"truncated", routeResult.Truncated, runnerResult.Truncated},
		{"service", routeResult.Service, runnerResult.Service},
		{"query", routeResult.Query, runnerResult.Query},
	} {
		if field.route != field.runner {
			t.Fatalf("%s drifted: route=%v runner=%v", field.name, field.route, field.runner)
		}
	}
	if routeResult.Stored == 0 {
		t.Fatal("both paths must actually store something for this to prove anything")
	}
}

// A service whose connector cannot sync the archive stays a 501 on the route.
func TestMailSyncRoute_KeepsNotImplementedContract(t *testing.T) {
	a := openTestMailArchive(t)
	srv := mailTestServer(t, a)
	srv.connectors["dummy"] = &stubNoSyncConnector{}
	srv.config.Services = append(srv.config.Services, Service{
		ID: "dummy", Name: "Dummy", Action: "inject",
	})
	w := doAdminMailReq(t, srv, http.MethodPost, "/v1/admin/mail/sync", `{"service":"dummy"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501 (the runner's terminal error must not become a 200)", w.Code)
	}
	// The queue path still runs the same code and reports it as terminal.
	res, err := srv.doMailIngest(context.Background(), mailIngestPayload{Service: "dummy"})
	if err == nil || !strings.Contains(err.Error(), syncjobs.ErrSyncTerminal.Error()) {
		t.Fatalf("runner err = %v, want terminal (res %+v)", err, res)
	}
}

func TestParseMailIngestPayload_JobTypeDefaults(t *testing.T) {
	// import_snapshot defaults to the Takeout export, not Gmail.
	p, err := parseMailIngestPayload(syncjobs.SyncJob{JobType: syncjobs.SyncJobTypeImport})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != connectors.TakeoutServiceID {
		t.Fatalf("import_snapshot service = %q, want %q", p.Service, connectors.TakeoutServiceID)
	}
	p, err = parseMailIngestPayload(syncjobs.SyncJob{JobType: syncjobs.SyncJobTypeIngest})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != connectors.GoogleGmailServiceID {
		t.Fatalf("ingest service = %q, want google-gmail", p.Service)
	}
	// An explicit service always wins.
	p, err = parseMailIngestPayload(syncjobs.SyncJob{
		JobType: syncjobs.SyncJobTypeIngest, Service: "x",
		Payload: json.RawMessage(`{"service":"y"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Service != "y" {
		t.Fatalf("service = %q, want the payload's y", p.Service)
	}
	if _, err := parseMailIngestPayload(syncjobs.SyncJob{Payload: json.RawMessage(`{`)}); err == nil {
		t.Fatal("bad payload must fail terminal")
	}
}

func TestMailIngestRunner_ServesBothJobTypes(t *testing.T) {
	r := &mailIngestRunner{srv: &Server{}}
	got := r.JobTypes()
	if len(got) != 2 || got[0] != syncjobs.SyncJobTypeIngest || got[1] != syncjobs.SyncJobTypeImport {
		t.Fatalf("JobTypes = %v, want ingest and import_snapshot", got)
	}
	reg := syncjobs.NewRunnerRegistry()
	reg.Register(r)
	if _, ok := reg.For(mailIngestKind, syncjobs.SyncJobTypeIngest); !ok {
		t.Fatal("ingest must resolve")
	}
	if _, ok := reg.For(mailIngestKind, syncjobs.SyncJobTypeImport); !ok {
		t.Fatal("import_snapshot must resolve (it is no longer a dead type)")
	}
}
