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
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
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
	srv.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{
		AccessToken: "access-token", Expiry: time.Now().Add(time.Hour),
	})
	store := testJobStore(t)
	srv.syncJobs = store
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = NewSyncWorker(store, srv.syncRunners, SyncWorkerOptions{Workers: 1, Prom: srv.prom})
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
	if step.RetryAfter != connectors.GmailQuotaBackoff {
		t.Fatalf("step.RetryAfter = %v, want %v", step.RetryAfter, connectors.GmailQuotaBackoff)
	}
	var ra *SyncRetryAfter
	if !errors.As(err, &ra) || ra.After != connectors.GmailQuotaBackoff {
		t.Fatalf("err = %v, want a SyncRetryAfter of %v", err, connectors.GmailQuotaBackoff)
	}
	class, after := classifySyncError(err, 1, step.RetryAfter)
	if class != SyncErrorClassRetry || after != connectors.GmailQuotaBackoff {
		t.Fatalf("classify = (%s,%v), want (retryable,%v)", class, after, connectors.GmailQuotaBackoff)
	}
}

func TestMailIngestRunner_TakeoutStep(t *testing.T) {
	srv, store, w := mailRunnerServer(t, nil)

	// No takeout path configured is terminal.
	_, err := srv.doMailIngest(context.Background(), mailIngestPayload{Service: connectors.TakeoutServiceID})
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
	w := doMailReq(t, srv1, http.MethodPost, "/v1/mail/sync", `{"max_messages":5,"reset_cursor":true}`)
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
		GrantLimits: policy.Permissions{"messages": "read"},
	})
	w := doMailReq(t, srv, http.MethodPost, "/v1/mail/sync", `{"service":"dummy"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501 (the runner's terminal error must not become a 200)", w.Code)
	}
	// The queue path still runs the same code and reports it as terminal.
	res, err := srv.doMailIngest(context.Background(), mailIngestPayload{Service: "dummy"})
	if err == nil || !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("runner err = %v, want terminal (res %+v)", err, res)
	}
}

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
	r := &mailIngestRunner{srv: &Server{}}
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

func TestJobStore_RenewLease(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	claimed, ok, _ := s.Claim("w1", s.now().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}

	// The owner may extend its own lease.
	far := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	kept, err := s.RenewLease(claimed.ID, "w1", far)
	if err != nil || !kept {
		t.Fatalf("RenewLease(owner) = (%v,%v), want (true,nil)", kept, err)
	}
	after, _ := s.GetByID(claimed.ID)
	if !after.LeaseUntil.Equal(far) {
		t.Fatalf("lease_until = %v, want %v", after.LeaseUntil, far)
	}

	// A different owner cannot extend it.
	kept, err = s.RenewLease(claimed.ID, "w2", far.Add(time.Hour))
	if err != nil {
		t.Fatalf("RenewLease(other): %v", err)
	}
	if kept {
		t.Fatal("a non-owner must not be able to renew")
	}

	// A job that is no longer running cannot be renewed.
	if _, err := s.Cancel(claimed.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	kept, err = s.RenewLease(claimed.ID, "w1", far.Add(2*time.Hour))
	if err != nil || kept {
		t.Fatalf("RenewLease(canceled) = (%v,%v), want (false,nil)", kept, err)
	}

	// Disabled store and empty owner are no-ops / errors.
	off, _ := OpenJobStore(nil, "")
	if kept, _ := off.RenewLease("1", "w", time.Now()); kept {
		t.Fatal("disabled renewal must report false")
	}
	if _, err := s.RenewLease(claimed.ID, "  ", time.Now()); err == nil {
		t.Fatal("renewal requires an owner")
	}
}

// When renewal reports the lease is gone, the in-flight step is wound down
// rather than writing against a job someone else now owns.
func TestSyncWorker_LeaseKeeperCancelsOnLoss(t *testing.T) {
	store := testJobStore(t)
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	reg := NewRunnerRegistry()
	started := make(chan struct{})
	stopped := make(chan struct{}, 1)
	reg.Register(&stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			close(started)
			<-ctx.Done()
			stopped <- struct{}{}
			return SyncStep{}, ctx.Err()
		},
	})
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	// Speed the keeper up so the test does not wait a third of a lease.
	w.now = store.now

	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Run the job through the worker so keepLeaseAlive is armed.
		w.RunOnce(context.Background(), "owner-a")
	}()
	<-started

	job := store.List(SyncFilter{})[0]
	// Someone else steals the job, exactly as an expired lease would allow.
	if _, err := store.ReclaimExpired(store.now().Add(syncJobLease + time.Minute)); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if _, ok, _ := store.Claim("owner-b", store.now().Add(syncJobLease+2*time.Minute)); !ok {
		t.Fatal("expected the reclaimed job to be claimable by another owner")
	}

	// Fire the keeper's tick by moving its clock far enough ahead, then ask
	// it to renew: the renewal must fail and cancel the step.
	kept, err := store.RenewLease(job.ID, "owner-a", store.now().Add(time.Hour))
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if kept {
		t.Fatal("a lost lease must not renew")
	}
	// The keeper observes that on its next tick and cancels; cancel directly
	// here to assert the wind-down contract without waiting on the ticker.
	w.CancelJob(job.ID)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the step did not wind down after losing the lease")
	}
	<-done

	final, _ := store.GetByID(job.ID)
	if final.LeaseOwner == "owner-a" {
		t.Fatal("owner-a must no longer hold the lease")
	}
}
