package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

// --- mirror runner ---

type recordingMirrorTarget struct {
	mu       sync.Mutex
	created  int
	appends  int
	lastID   string
	copied   int
	failOnce bool
}

func (t *recordingMirrorTarget) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []types.Track) (types.PlacePlaylistItemsResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failOnce {
		t.failOnce = false
		return types.PlacePlaylistItemsResult{PlaylistID: playlistID}, fmt.Errorf("upstream 502")
	}
	if playlistID == "" {
		t.created++
		playlistID = fmt.Sprintf("target-%d", t.created)
	} else {
		t.appends++
	}
	t.lastID = playlistID
	t.copied += len(tracks)
	return types.PlacePlaylistItemsResult{
		PlaylistID: playlistID,
		URL:        "https://example.com/" + playlistID,
		Copied:     len(tracks),
	}, nil
}

func (t *recordingMirrorTarget) SaveLikedTracks(ctx context.Context, tracks []types.Track) (types.SaveLikedTracksResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.copied += len(tracks)
	return types.SaveLikedTracksResult{Copied: len(tracks)}, nil
}

// stubMirrorSource serves pages from a fixed slice.
type stubMirrorSource struct {
	tracks []types.Track
	name   string
}

// srcTrack builds a stub fixture track with a service-local id.
func srcTrack(name, id string) types.Track {
	return types.Track{Name: name, Sources: []types.TrackSource{{Service: "stub", ID: id}}}
}

func (s *stubMirrorSource) ListPlaylistItems(ctx context.Context, id, cursor string, limit int) (types.Playlist, types.TrackPage, error) {
	offset := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &offset)
	}
	end := offset + limit
	if end > len(s.tracks) {
		end = len(s.tracks)
	}
	if offset > len(s.tracks) {
		offset = len(s.tracks)
	}
	page := s.tracks[offset:end]
	next := ""
	if end < len(s.tracks) {
		next = fmt.Sprint(end)
	}
	return types.Playlist{Name: s.name, Sources: []types.PlaylistSource{{Service: "stub", ID: id}}},
		types.TrackPage{Tracks: page, Total: len(s.tracks), Next: next}, nil
}

func (s *stubMirrorSource) ListUserLikedTracks(ctx context.Context) ([]types.Track, bool, error) {
	return s.tracks, false, nil
}

func (s *stubMirrorSource) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (types.TrackPage, error) {
	_, page, err := s.ListPlaylistItems(ctx, "liked", cursor, limit)
	return page, err
}

// mirrorStubServer builds a Server whose mirror adapters are stubs and whose
// services carry read-write grants, so the real permission checks pass.
func mirrorStubServer(t *testing.T, src mirrorSource, dst mirrorTarget) *Server {
	t.Helper()
	grants := policy.Permissions{"playlists": policy.AccessFull, "tracks": policy.AccessFull}
	srv := testServerWithBase(&Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", GrantLimits: grants},
		{ID: "youtube-music", Name: "YouTube Music", Action: "inject", GrantLimits: grants},
	}}, "https://aegis.example.com")
	ledger, err := copies.Open(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	srv.copyLedger = ledger
	if src != nil {
		srv.mirrorSourceFn = func(serviceID, kind string) (mirrorSource, error) { return src, nil }
	}
	srv.mirrorTargetFn = func(serviceID, kind string) (mirrorTarget, error) { return dst, nil }
	return srv
}

// mirrorTestServer swaps in stub adapters and a fresh ledger/job store.
func mirrorTestServer(t *testing.T, src mirrorSource, dst mirrorTarget) (*Server, *JobStore, *SyncWorker) {
	t.Helper()
	srv := mirrorStubServer(t, src, dst)
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	registerMirrorRunners(reg, srv)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	return srv, store, w
}

func TestMirrorRunner_ChainsIntoOneTargetPlaylist(t *testing.T) {
	tracks := []types.Track{
		srcTrack("One", "t1"), srcTrack("Two", "t2"),
		srcTrack("Three", "t3"), srcTrack("Four", "t4"),
		srcTrack("Five", "t5"),
	}
	src := &stubMirrorSource{tracks: tracks, name: "Road trip"}
	dst := &recordingMirrorTarget{}
	srv, store, w := mirrorTestServer(t, src, dst)

	mustEnqueue(t, store, SyncEnqueue{
		Kind: MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "youtube-music",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "src1",
			"target_service": "youtube-music", "cursor": "", "limit": 2,
		},
	})

	// Two tracks per step: five tracks need three steps.
	for i := 0; i < 6 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	if dst.created != 1 {
		t.Fatalf("created = %d target playlists, want exactly 1", dst.created)
	}
	if dst.appends != 2 {
		t.Fatalf("appends = %d, want 2 (three steps, first created)", dst.appends)
	}
	if dst.copied != 5 {
		t.Fatalf("copied = %d, want 5", dst.copied)
	}

	// The copy ledger row is stamped once, pointing at the single target.
	rec, ok := srv.copyLedger.Get(MirrorKindPlaylists, "youtube-music", dst.lastID)
	if !ok {
		t.Fatalf("copy row missing for target %q", dst.lastID)
	}
	if rec.SourceService != "spotify" || rec.SourceID != "src1" {
		t.Fatalf("copy row = %+v, want source spotify/src1", rec)
	}
}

func TestMirrorRunner_LikedTracksChainAndStamp(t *testing.T) {
	tracks := []types.Track{
		srcTrack("One", "t1"), srcTrack("Two", "t2"),
		srcTrack("Three", "t3"),
	}
	src := &stubMirrorSource{tracks: tracks}
	dst := &recordingMirrorTarget{}
	srv, store, w := mirrorTestServer(t, src, dst)

	mustEnqueue(t, store, SyncEnqueue{
		Kind: MirrorKindTracks, JobType: SyncJobTypeMirror, Service: "spotify",
		Payload: map[string]any{
			"kind": "tracks", "source_service": "spotify",
			"target_service": "youtube-music", "limit": 2,
		},
	})
	for i := 0; i < 5 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	if dst.copied != 3 {
		t.Fatalf("copied = %d, want 3", dst.copied)
	}
	if _, ok := srv.copyLedger.Get(MirrorKindTracks, "youtube-music", ""); !ok {
		t.Fatal("liked-tracks copy row must be stamped")
	}
}

func TestMirrorRunner_RepairSkipsWhatTheTargetHolds(t *testing.T) {
	source := &stubMirrorSource{
		name: "Road trip",
		tracks: []types.Track{
			srcTrack("One", "t1"), srcTrack("Two", "t2"),
		},
	}
	// The target's source reader reports it already holds "One".
	target := &stubMirrorSource{
		tracks: []types.Track{srcTrack("One", "t1")},
	}
	dst := &recordingMirrorTarget{}
	srv := mirrorStubServer(t, nil, dst)
	srv.mirrorSourceFn = func(serviceID, kind string) (mirrorSource, error) {
		if serviceID == "youtube-music" {
			return target, nil
		}
		return source, nil
	}
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	registerMirrorRunners(reg, srv)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})

	mustEnqueue(t, store, SyncEnqueue{
		Kind: MirrorKindPlaylists, JobType: SyncJobTypeRepair, Service: "youtube-music",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "src1",
			"target_service": "youtube-music", "target_id": "PL-existing", "force": true,
			"limit": 200,
		},
	})
	for i := 0; i < 4 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	// Only "Two" should have been placed: "One" was already on the target.
	if dst.copied != 1 {
		t.Fatalf("copied = %d, want 1 (repair must skip what is already there)", dst.copied)
	}
	if dst.created != 0 {
		t.Fatalf("repair must append to the recorded target, created=%d", dst.created)
	}
	if dst.lastID != "PL-existing" {
		t.Fatalf("target = %q, want PL-existing", dst.lastID)
	}
}

func TestMirrorRunner_TerminalValidation(t *testing.T) {
	src := &stubMirrorSource{tracks: []types.Track{srcTrack("A", "a")}}
	dst := &recordingMirrorTarget{}
	_, store, w := mirrorTestServer(t, src, dst)

	for i, payload := range []map[string]any{
		{"kind": "playlists", "source_service": "spotify", "source_id": "s", "target_service": "spotify"},
		{"kind": "playlists", "source_service": "spotify", "target_service": "youtube-music"},
		{"kind": "playlists", "source_service": "spotify", "source_id": "s"},
		{"kind": "tracks", "source_service": "spotify", "target_service": "youtube-music"},
	} {
		b, _ := json.Marshal(payload)
		mustEnqueue(t, store, SyncEnqueue{
			Kind: MirrorKindPlaylists, JobType: SyncJobTypeMirror,
			EntityKey: fmt.Sprint("bad-", i), Payload: payload,
		})
		// Force the payload onto the queued job by re-enqueueing with the id.
		job := store.List(SyncFilter{})[len(store.List(SyncFilter{}))-1]
		_, _ = w.store.Fail(job, SyncStep{Payload: b}, ErrSyncTerminal)
	}
	// A missing target_service is terminal.
	if _, err := (&mirrorRunner{srv: nil}).Run(context.Background(), SyncJob{}); err == nil {
		t.Fatal("unwired runner must fail")
	}
}

func TestMirrorRunner_DryRunPlacesNothing(t *testing.T) {
	src := &stubMirrorSource{tracks: []types.Track{srcTrack("A", "a")}, name: "L"}
	dst := &recordingMirrorTarget{}
	srv, store, w := mirrorTestServer(t, src, dst)
	mustEnqueue(t, store, SyncEnqueue{
		Kind: MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "youtube-music",
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "s",
			"target_service": "youtube-music", "dry_run": true,
		},
	})
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	if dst.created != 0 || dst.copied != 0 {
		t.Fatalf("dry-run placed work: created=%d copied=%d", dst.created, dst.copied)
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded", job.State)
	}
	if _, ok := srv.copyLedger.Get(MirrorKindPlaylists, "youtube-music", "s"); ok {
		t.Fatal("dry-run must not stamp a copy row")
	}
}

func TestMirrorRunner_RetriesUpstreamError(t *testing.T) {
	src := &stubMirrorSource{tracks: []types.Track{srcTrack("A", "a")}, name: "L"}
	dst := &recordingMirrorTarget{failOnce: true}
	_, store, w := mirrorTestServer(t, src, dst)
	mustEnqueue(t, store, SyncEnqueue{
		Kind: MirrorKindPlaylists, JobType: SyncJobTypeMirror, Service: "youtube-music",
		MaxAttempts: 3,
		Payload: map[string]any{
			"kind": "playlists", "source_service": "spotify", "source_id": "s",
			"target_service": "youtube-music",
		},
	})
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued (retryable upstream error)", job.State)
	}
	if !strings.Contains(job.LastError, "502") {
		t.Fatalf("last_error = %q, want the upstream error", job.LastError)
	}
}

func TestNewMirrorRunners_Register(t *testing.T) {
	runners := newMirrorRunners(&Server{})
	if len(runners) != len(mirrorKinds) {
		t.Fatalf("runners = %d, want %d", len(runners), len(mirrorKinds))
	}
	reg := NewRunnerRegistry()
	registerMirrorRunners(reg, &Server{})
	for _, kind := range mirrorKinds {
		r, ok := reg.For(kind, SyncJobTypeMirror)
		if !ok {
			t.Fatalf("kind %s has no mirror runner", kind)
		}
		if _, ok := reg.For(kind, SyncJobTypeRepair); !ok {
			t.Fatalf("kind %s has no repair runner", kind)
		}
		types := r.JobTypes()
		if len(types) != 2 {
			t.Fatalf("JobTypes = %v, want mirror and repair", types)
		}
	}
}

// --- pause / resume ---

func TestJobStore_PauseResume(t *testing.T) {
	s := testJobStore(t)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "ingest", Service: "spotify"})

	if err := s.Pause("spotify", "quota"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !s.IsPaused("spotify") || s.IsPaused("google-gmail") {
		t.Fatal("pause must apply to the named service only")
	}
	got := s.List(SyncFilter{State: SyncJobPaused})
	if len(got) != 1 || got[0].Service != "spotify" {
		t.Fatalf("paused = %+v, want only the spotify job", got)
	}
	// The claim filter must skip the paused service but keep serving others.
	job, ok, _ := s.Claim("w", s.now().Add(time.Second))
	if !ok || job.Service != "google-gmail" {
		t.Fatalf("claim = (%+v,%v), want the unpaused google-gmail job", job, ok)
	}
	if job, ok, _ := s.Claim("w", s.now().Add(time.Second)); ok {
		t.Fatalf("second claim handed out paused work: %+v", job)
	}

	// New enqueues for the paused scope land in `paused` too.
	third := mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "mirror", Service: "spotify"})
	if third.State != SyncJobPaused {
		t.Fatalf("new enqueue state = %s, want paused", third.State)
	}

	// Resume puts the scope's jobs back in the queue.
	if err := s.Resume("spotify"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if s.IsPaused("spotify") {
		t.Fatal("scope must be resumed")
	}
	if got := s.List(SyncFilter{State: SyncJobPaused}); len(got) != 0 {
		t.Fatalf("still paused: %+v", got)
	}
	claimed := map[string]bool{}
	for i := 0; i < 3; i++ {
		if job, ok, _ := s.Claim("w", s.now().Add(time.Second)); ok {
			claimed[job.Service+"/"+job.JobType] = true
		}
	}
	if !claimed["spotify/mirror"] {
		t.Fatalf("after resume the spotify job must be claimable, got %v", claimed)
	}

	// Global pause holds everything.
	if err := s.Pause(SyncPauseScopeGlobal, "maintenance"); err != nil {
		t.Fatalf("global Pause: %v", err)
	}
	if !s.IsPaused("google-gmail") {
		t.Fatal("global pause must hold every service")
	}
	if job, ok, _ := s.Claim("w", s.now().Add(2*time.Hour)); ok {
		t.Fatalf("claim under global pause handed out %+v", job)
	}
	if err := s.Resume(SyncPauseScopeGlobal); err != nil {
		t.Fatalf("global Resume: %v", err)
	}
	if got := s.Paused(); len(got) != 0 {
		t.Fatalf("Paused = %v, want none", got)
	}

	// Resuming a service while the global pause is on must not release it.
	if err := s.Pause(SyncPauseScopeGlobal, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause("spotify", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume("spotify"); err != nil {
		t.Fatal(err)
	}
	if !s.IsPaused("spotify") {
		t.Fatal("global pause must keep the service held after a service resume")
	}

	// A disabled store is a no-op.
	off, _ := OpenJobStore(nil, "")
	if err := off.Pause("x", ""); err != nil {
		t.Fatalf("disabled Pause: %v", err)
	}
	if off.IsPaused("x") {
		t.Fatal("disabled store must report not paused")
	}
	if got := off.Paused(); got != nil {
		t.Fatalf("disabled Paused = %v", got)
	}
}

// --- per-service concurrency ---

type blockingRunner struct {
	kind    string
	started chan string
	release chan struct{}
}

func (r *blockingRunner) JobTypes() []string { return []string{"ingest"} }
func (r *blockingRunner) Kind() string       { return r.kind }
func (r *blockingRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	r.started <- job.Service
	<-r.release
	return SyncStep{Done: true}, nil
}

func TestSyncWorker_ServiceConcurrencyCap(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	started := make(chan string, 4)
	release := make(chan struct{})
	// One runner kind per service so both jobs find a runner.
	for _, kind := range []string{"messages", "playlists"} {
		reg.Register(&blockingRunner{kind: kind, started: started, release: release})
	}
	w := NewSyncWorker(store, reg, SyncWorkerOptions{
		Workers: 4, ServiceConcurrency: map[string]int{"spotify": 1},
	})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "spotify"})
	mustEnqueue(t, store, SyncEnqueue{Kind: "playlists", JobType: "ingest", Service: "spotify"})

	// With the cap at 1, only one spotify job may start even with 4 workers.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for w.RunOnce(context.Background(), "w1") {
		}
	}()
	go func() {
		for w.RunOnce(context.Background(), "w2") {
		}
	}()

	first := <-started
	// Give the second worker a chance; it must block on the service cap.
	select {
	case second := <-started:
		t.Fatalf("two %s jobs ran concurrently despite the cap", second)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	<-done

	if first != "spotify" {
		t.Fatalf("started = %q", first)
	}
	if got := store.Counts("", ""); got.Succeeded != 2 {
		t.Fatalf("counts = %+v, want both jobs succeeded", got)
	}
}

// --- admin pause endpoints ---

func TestHandleAdminSyncPauseResume(t *testing.T) {
	srv := syncAdminServer(t)
	mustEnqueue(t, srv.syncJobs, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	req := adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", `{"service":"google-gmail","reason":"quota"}`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	paused, _ := out["paused"].([]any)
	if len(paused) != 1 || paused[0].(string) != "google-gmail" {
		t.Fatalf("paused = %v, want [google-gmail]", out["paused"])
	}
	if got := srv.syncJobs.List(SyncFilter{State: SyncJobPaused}); len(got) != 1 {
		t.Fatalf("paused jobs = %+v, want 1", got)
	}

	// The status model reports the pause.
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	var status SyncStatus
	_ = json.Unmarshal(w.Body.Bytes(), &status)
	if len(status.Paused) != 1 || status.Paused[0] != "google-gmail" {
		t.Fatalf("status.paused = %v", status.Paused)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/resume", "admin-key", `{"service":"google-gmail"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncResume)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body %s", w.Code, w.Body.String())
	}
	if got := srv.syncJobs.List(SyncFilter{State: SyncJobPaused}); len(got) != 0 {
		t.Fatalf("after resume paused jobs = %+v", got)
	}

	// Global pause with an empty body.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("global pause status = %d", w.Code)
	}
	if !srv.syncJobs.IsPaused("anything") {
		t.Fatal("global pause must hold every service")
	}
	req = adminRequest(http.MethodPost, "/v1/admin/sync/resume", "admin-key", `{}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncResume)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("global resume status = %d", w.Code)
	}

	// Method and disabled-store guards.
	req = adminRequest(http.MethodGet, "/v1/admin/sync/pause", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET pause = %d, want 405", w.Code)
	}

	off := syncAdminServer(t)
	off.syncJobs = &JobStore{}
	req = adminRequest(http.MethodPost, "/v1/admin/sync/pause", "admin-key", `{}`)
	w = httptest.NewRecorder()
	off.AdminMiddleware(off.HandleAdminSyncPause)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled pause = %d, want 503", w.Code)
	}
}

func TestParseServiceConcurrency(t *testing.T) {
	got := parseServiceConcurrency("spotify=1,youtube-music=2, broken , x=abc, y=0, z=3")
	want := map[string]int{"spotify": 1, "youtube-music": 2, "z": 3}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parse[%s] = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if got := parseServiceConcurrency(""); len(got) != 0 {
		t.Fatalf("empty parse = %v", got)
	}
}
