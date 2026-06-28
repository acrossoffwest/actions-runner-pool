package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/scheduler"
	"github.com/muhac/actions-runner-pool/internal/store"
)

const testWebhookSecret = "shh-it-is-a-secret"

type spyEnqueuer struct {
	enqueued []int64
	calls    atomic.Int64
}

func (s *spyEnqueuer) Enqueue(jobID int64) {
	s.calls.Add(1)
	s.enqueued = append(s.enqueued, jobID)
}

func newWebhookHandler(t *testing.T, st store.Store, sch *spyEnqueuer, runnerLabels []string) *WebhookHandler {
	return newWebhookHandlerWithDynamicPrefixes(t, st, sch, runnerLabels, []string{"gharp-"})
}

func newWebhookHandlerWithDynamicPrefixes(t *testing.T, st store.Store, sch *spyEnqueuer, runnerLabels []string, dynamicPrefixes []string) *WebhookHandler {
	t.Helper()
	// Mirror what config.Load does: precompute the lower-cased label
	// set so the handler reads RunnerLabelSet, not RunnerLabels.
	set := make(map[string]struct{}, len(runnerLabels))
	for _, l := range runnerLabels {
		set[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
	}
	for i, prefix := range dynamicPrefixes {
		dynamicPrefixes[i] = strings.ToLower(strings.TrimSpace(prefix))
	}
	return &WebhookHandler{
		Cfg: &config.Config{
			BaseURL:                    "https://example.test",
			RunnerLabels:               runnerLabels,
			RunnerLabelSet:             set,
			RunnerDynamicLabelPrefixes: dynamicPrefixes,
		},
		Store:     st,
		Scheduler: sch,
	}
}

func storeWithSecret(secret string) *fakeStore {
	// OwnerLogin "alice" matches the "alice/..." repos used in existing tests
	// so they pass the owner-allowlist gate without extra setup.
	return &fakeStore{appConfig: &store.AppConfig{WebhookSecret: secret, OwnerLogin: "alice"}}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, h *WebhookHandler, event string, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rr := httptest.NewRecorder()
	h.Post(rr, req)
	return rr
}

func TestWebhook_BadSignature_401(t *testing.T) {
	h := newWebhookHandler(t, storeWithSecret(testWebhookSecret), &spyEnqueuer{}, nil)
	body := []byte(`{"action":"queued"}`)
	rr := postWebhook(t, h, "workflow_job", body, "sha256=deadbeef")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestWebhook_NoAppConfig_503(t *testing.T) {
	st := &fakeStore{} // appConfig nil
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	body := []byte(`{}`)
	rr := postWebhook(t, h, "ping", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestWebhook_PingPasses_200(t *testing.T) {
	body := []byte(`{"zen":"hi"}`)
	h := newWebhookHandler(t, storeWithSecret(testWebhookSecret), &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "ping", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestWebhook_UnknownEvent_200NoOp(t *testing.T) {
	body := []byte(`{}`)
	h := newWebhookHandler(t, storeWithSecret(testWebhookSecret), &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "push", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestWebhook_InstallationCreated_Upserts(t *testing.T) {
	body := []byte(`{
		"action": "created",
		"installation": {
			"id": 99,
			"account": {"id": 7, "login": "alice", "type": "User"}
		},
		"repositories": [{"full_name": "alice/repo1"}, {"full_name": "alice/repo2"}]
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "installation", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.upsertedInstallations) != 1 || st.upsertedInstallations[0].ID != 99 {
		t.Errorf("UpsertInstallation not called: %+v", st.upsertedInstallations)
	}
	if got := st.upsertedRepoInstallation; got["alice/repo1"] != 99 || got["alice/repo2"] != 99 {
		t.Errorf("UpsertRepoInstallation: %+v", got)
	}
}

func TestWebhook_InstallationRepositoriesAddedRemoved(t *testing.T) {
	body := []byte(`{
		"action": "added",
		"installation": {"id": 99},
		"repositories_added": [{"full_name": "alice/new"}],
		"repositories_removed": [{"full_name": "alice/gone"}]
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "installation_repositories", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if st.upsertedRepoInstallation["alice/new"] != 99 {
		t.Errorf("expected upsert alice/new -> 99, got %+v", st.upsertedRepoInstallation)
	}
	if len(st.removedRepoInstallation) != 1 || st.removedRepoInstallation[0] != "alice/gone" {
		t.Errorf("expected remove alice/gone, got %+v", st.removedRepoInstallation)
	}
	// New behavior: removed repos must also have their pending jobs
	// cancelled, otherwise dispatch keeps trying to mint a token for
	// an installation that no longer covers them.
	if len(st.cancelledForRepo) != 1 || st.cancelledForRepo[0] != "alice/gone" {
		t.Errorf("expected CancelPendingJobsForRepo(alice/gone), got %+v", st.cancelledForRepo)
	}
}

// installation:deleted means the App was uninstalled. We must (a)
// drop the repo->installation rows for every covered repo so dispatch
// stops trying to mint tokens, and (b) cancel any still-dispatchable
// jobs so they don't sit pending forever.
func TestWebhook_InstallationDeleted_CancelsAndUnmaps(t *testing.T) {
	body := []byte(`{
		"action": "deleted",
		"installation": {"id": 99, "account": {"id": 7, "login": "alice", "type": "User"}},
		"repositories": [{"full_name": "alice/repo1"}, {"full_name": "alice/repo2"}]
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "installation", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.cancelledForRepo) != 2 {
		t.Fatalf("expected 2 CancelPendingJobsForRepo calls, got %+v", st.cancelledForRepo)
	}
	if len(st.removedRepoInstallation) != 2 {
		t.Fatalf("expected 2 RemoveRepoInstallation calls, got %+v", st.removedRepoInstallation)
	}
}

const queuedJobBody = `{
	"action": "queued",
	"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
	"repository": {"full_name": "alice/repo", "private": true},
	"installation": {"id": 99}
}`

func TestWebhook_QueuedHappyPath_InsertsAndEnqueues(t *testing.T) {
	body := []byte(queuedJobBody)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil) // nil = serve everything
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 1 || st.insertedJobs[0].ID != 12345 {
		t.Errorf("InsertJobIfNew not called as expected: %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 1 || sch.enqueued[0] != 12345 {
		t.Errorf("Enqueue: calls=%d enqueued=%v", sch.calls.Load(), sch.enqueued)
	}
	// Lazy-write: repo->installation should also be set.
	if st.upsertedRepoInstallation["alice/repo"] != 99 {
		t.Errorf("expected lazy upsert repo->installation, got %+v", st.upsertedRepoInstallation)
	}
}

func TestWebhook_QueuedDuplicate_DedupedAtStore(t *testing.T) {
	body := []byte(queuedJobBody)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)

	for range 2 {
		rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
	}
	if sch.calls.Load() != 1 {
		t.Errorf("Enqueue called %d times, want 1 (dedup at store)", sch.calls.Load())
	}
}

func TestWebhook_QueuedStoreError_503AndNoEnqueue(t *testing.T) {
	body := []byte(queuedJobBody)
	st := storeWithSecret(testWebhookSecret)
	st.insertJobErr = errors.New("disk on fire")
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if sch.calls.Load() != 0 {
		t.Errorf("Enqueue should not be called on store error")
	}
}

func TestWebhook_QueuedStrangerOwner_Dropped(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 777, "labels": ["self-hosted"]},
		"repository": {"full_name": "evil/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret) // OwnerLogin "alice" — "evil" is a stranger
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if sch.calls.Load() != 0 {
		t.Fatalf("stranger owner must not be enqueued, calls=%d", sch.calls.Load())
	}
}

func TestWebhook_PublicRepo_DefaultDrops(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 0 {
		t.Errorf("expected no insert, got %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 0 {
		t.Errorf("expected no Enqueue, got %d", sch.calls.Load())
	}
	if len(st.upsertedRepoInstallation) != 0 {
		t.Errorf("expected no lazy repo upsert, got %+v", st.upsertedRepoInstallation)
	}
}

func TestWebhook_InternalRepo_DefaultProceeds(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/internal", "private": false, "visibility": "internal"},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 1 || st.insertedJobs[0].Repo != "alice/internal" {
		t.Errorf("InsertJobIfNew not called as expected: %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 1 {
		t.Errorf("expected Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_PublicRepo_AllowPublicReposProceeds(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	h.Cfg.AllowPublicRepos = true
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 1 || st.insertedJobs[0].Repo != "alice/public" {
		t.Errorf("InsertJobIfNew not called as expected: %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 1 {
		t.Errorf("expected Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_PublicRepo_AllowlistBypassesGuard(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
		"repository": {"full_name": "Alice/Public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	h.Cfg.RepoAllowlistSet = map[string]struct{}{"alice/public": {}}
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 1 || st.insertedJobs[0].Repo != "Alice/Public" {
		t.Errorf("InsertJobIfNew not called as expected: %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 1 {
		t.Errorf("expected Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_PublicRepo_AllowlistMismatchDrops(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	h.Cfg.RepoAllowlistSet = map[string]struct{}{"alice/other": {}}
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 0 {
		t.Errorf("expected no insert, got %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 0 {
		t.Errorf("expected no Enqueue, got %d", sch.calls.Load())
	}
}

// Pool advertises [linux] but a job requires [self-hosted, gpu] →
// must be rejected because gpu is not satisfiable. Pre-superset, this
// also failed (no overlap). Post-superset, the rejection mechanism
// changed but the outcome is the same.
func TestWebhook_LabelFilter_DropsNonMatching(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12346, "labels": ["self-hosted", "gpu"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, []string{"linux"}) // gpu unsatisfiable
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 0 {
		t.Errorf("expected no insert, got %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 0 {
		t.Errorf("expected no Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_LabelFilter_MatchProceeds(t *testing.T) {
	body := []byte(queuedJobBody)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, []string{"self-hosted", "gpu"})
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if sch.calls.Load() != 1 {
		t.Errorf("expected Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_LabelFilter_DynamicPrefixProceeds(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted", "gharp-build-123-1"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, []string{"self-hosted"})
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 1 || st.insertedJobs[0].Labels != "self-hosted,gharp-build-123-1" {
		t.Errorf("InsertJobIfNew not called as expected: %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 1 {
		t.Errorf("expected Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_LabelFilter_UnknownDynamicPrefixDrops(t *testing.T) {
	body := []byte(`{
		"action": "queued",
		"workflow_job": {"id": 12345, "labels": ["self-hosted", "other-build-123-1"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, []string{"self-hosted"})
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.insertedJobs) != 0 {
		t.Errorf("expected no insert, got %+v", st.insertedJobs)
	}
	if sch.calls.Load() != 0 {
		t.Errorf("expected no Enqueue, got %d", sch.calls.Load())
	}
}

func TestWebhook_InProgress_BindsRunnerAndMarksBusy(t *testing.T) {
	body := []byte(`{
		"action": "in_progress",
		"workflow_job": {"id": 12345, "runner_id": 77, "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	sch := &spyEnqueuer{}
	h := newWebhookHandler(t, st, sch, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedInProgress) != 1 ||
		st.markedInProgress[0].jobID != 12345 ||
		st.markedInProgress[0].runnerID != 77 ||
		st.markedInProgress[0].runnerName != "runner-A" {
		t.Errorf("MarkJobInProgress: %+v", st.markedInProgress)
	}
	if len(st.updatedRunnerByName) != 1 ||
		st.updatedRunnerByName[0].runnerName != "runner-A" ||
		st.updatedRunnerByName[0].status != "busy" {
		t.Errorf("UpdateRunnerStatusByName: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_PublicRepo_InProgressStillUpdatesLifecycle(t *testing.T) {
	body := []byte(`{
		"action": "in_progress",
		"workflow_job": {"id": 12345, "runner_id": 77, "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedInProgress) != 1 ||
		st.markedInProgress[0].jobID != 12345 ||
		st.markedInProgress[0].runnerName != "runner-A" {
		t.Errorf("MarkJobInProgress: %+v", st.markedInProgress)
	}
	if len(st.updatedRunnerByName) != 1 || st.updatedRunnerByName[0].status != "busy" {
		t.Errorf("UpdateRunnerStatusByName: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_InProgress_EmptyRunnerName_Skipped(t *testing.T) {
	// Observed in production: GitHub fires in_progress with runner_id=0
	// and runner_name="" when our gharp-launched runner lost the race
	// to a different runner (the runner↔job drift documented in
	// architecture.md). Without this skip, the row's status would
	// advance to in_progress and PendingJobs replay couldn't rescue it.
	body := []byte(`{
		"action": "in_progress",
		"workflow_job": {"id": 12345, "runner_id": 0, "runner_name": "", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedInProgress) != 0 {
		t.Errorf("MarkJobInProgress should not be called: %+v", st.markedInProgress)
	}
	if len(st.updatedRunnerByName) != 0 {
		t.Errorf("UpdateRunnerStatusByName should not be called: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_InProgress_NoOpWhenAlreadyAdvanced_DoesNotTouchRunner(t *testing.T) {
	// A late in_progress arriving after the row is already completed
	// must not flip the (now finished) runner back to busy. The fake's
	// markJobInProgressNoOp simulates the store's WHERE-status guard
	// returning advanced=false.
	body := []byte(`{
		"action": "in_progress",
		"workflow_job": {"id": 12345, "runner_id": 77, "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	st.markJobInProgressNoOp = true
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedInProgress) != 1 {
		t.Errorf("MarkJobInProgress should be called once: %+v", st.markedInProgress)
	}
	if len(st.updatedRunnerByName) != 0 {
		t.Errorf("runner status must NOT be touched on no-op advance: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_Completed_RecordsConclusion(t *testing.T) {
	body := []byte(`{
		"action": "completed",
		"workflow_job": {"id": 12345, "conclusion": "success", "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/repo", "private": true},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedCompleted) != 1 || st.markedCompleted[0].conclusion != "success" {
		t.Errorf("MarkJobCompleted: %+v", st.markedCompleted)
	}
	if len(st.updatedRunnerByName) != 1 || st.updatedRunnerByName[0].status != "finished" {
		t.Errorf("UpdateRunnerStatusByName: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_PublicRepo_CompletedStillUpdatesLifecycle(t *testing.T) {
	body := []byte(`{
		"action": "completed",
		"workflow_job": {"id": 12345, "conclusion": "success", "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedCompleted) != 1 || st.markedCompleted[0].conclusion != "success" {
		t.Errorf("MarkJobCompleted: %+v", st.markedCompleted)
	}
	if len(st.updatedRunnerByName) != 1 || st.updatedRunnerByName[0].status != "finished" {
		t.Errorf("UpdateRunnerStatusByName: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_PublicRepo_CompletedMissingJobDoesNotTouchRunner(t *testing.T) {
	body := []byte(`{
		"action": "completed",
		"workflow_job": {"id": 12345, "conclusion": "success", "runner_name": "runner-A", "labels": ["self-hosted"]},
		"repository": {"full_name": "alice/public", "private": false},
		"installation": {"id": 99}
	}`)
	st := storeWithSecret(testWebhookSecret)
	st.markJobCompletedNoOp = true
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(st.markedCompleted) != 1 {
		t.Errorf("MarkJobCompleted should be called once: %+v", st.markedCompleted)
	}
	if len(st.updatedRunnerByName) != 0 {
		t.Errorf("runner status must NOT be touched for missing job: %+v", st.updatedRunnerByName)
	}
}

func TestWebhook_BadJSON_400(t *testing.T) {
	body := []byte(`{not json`)
	h := newWebhookHandler(t, storeWithSecret(testWebhookSecret), &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, sign(testWebhookSecret, body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestWebhook_OversizeBody_413(t *testing.T) {
	// Body just over the 1MiB cap; signature can be anything because the
	// limit fires before HMAC verification.
	body := bytes.Repeat([]byte("x"), maxWebhookBodyBytes+1)
	h := newWebhookHandler(t, storeWithSecret(testWebhookSecret), &spyEnqueuer{}, nil)
	rr := postWebhook(t, h, "workflow_job", body, "sha256=whatever")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

func TestWebhook_EmptySecret_RejectsSignature(t *testing.T) {
	// Misconfiguration: app_config.WebhookSecret == "". An attacker computing
	// HMAC with an empty key would otherwise produce a "valid" signature.
	st := &fakeStore{appConfig: &store.AppConfig{WebhookSecret: ""}}
	h := newWebhookHandler(t, st, &spyEnqueuer{}, nil)
	body := []byte(`{}`)
	rr := postWebhook(t, h, "ping", body, sign("", body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (empty-secret rejection)", rr.Code)
	}
}

// spySender records the last SendMessage call for assertions.
type spySender struct {
	calls int
	token string
	chat  string
	text  string
	err   error
}

func (s *spySender) SendMessage(_ context.Context, token, chatID, text string) error {
	s.calls++
	s.token, s.chat, s.text = token, chatID, text
	return s.err
}

func newWorkflowRunBody(t *testing.T, action, conclusion string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": action,
		"workflow_run": map[string]any{
			"name": "CI", "conclusion": conclusion, "html_url": "https://x/runs/1",
			"head_branch": "main", "run_number": 7,
		},
		"repository": map[string]any{"full_name": "o/r"},
		"sender":     map[string]any{"login": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newWebhookHandlerWithNotify(t *testing.T, s *spySender, settings *store.NotifySettings) *WebhookHandler {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if settings != nil {
		if err := st.SaveNotifySettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
	}
	return &WebhookHandler{Store: st, Telegram: s, Log: slog.Default()}
}

func TestHandleWorkflowRun_Completed_Sends(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{
		Enabled: true, BotToken: "tok", ChatID: "99", Mode: "all",
	})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "success")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if s.calls != 1 || s.token != "tok" || s.chat != "99" {
		t.Fatalf("send = %+v", s)
	}
	if !strings.Contains(s.text, "o/r") || !strings.Contains(s.text, "run #7") {
		t.Fatalf("text = %q", s.text)
	}
}

func TestHandleWorkflowRun_NonCompleted_Ignored(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "requested", "")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK || s.calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, s.calls)
	}
}

func TestHandleWorkflowRun_Disabled_NoSend(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: false, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK || s.calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, s.calls)
	}
}

func TestHandleWorkflowRun_FailuresMode_SkipsSuccess(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "failures"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "success")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if s.calls != 0 {
		t.Fatalf("expected skip, calls=%d", s.calls)
	}
	// Failure in the same mode DOES send.
	body = newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if s.calls != 1 {
		t.Fatalf("expected one send for failure, calls=%d", s.calls)
	}
}

func TestHandleWorkflowRun_SendError_Still200(t *testing.T) {
	s := &spySender{err: errors.New("boom")}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (telegram failure must not fail the webhook)", rec.Code)
	}
}

func TestBuildRunMessage_FormatByConclusion(t *testing.T) {
	mk := func(concl string) *workflowRunEvent {
		ev := &workflowRunEvent{}
		ev.Action = "completed"
		ev.WorkflowRun.Name = "CI"
		ev.WorkflowRun.Conclusion = concl
		ev.WorkflowRun.HTMLURL = "https://x/runs/1"
		ev.WorkflowRun.HeadBranch = "main"
		ev.WorkflowRun.RunNumber = 7
		ev.Repository.FullName = "o/r"
		ev.Sender.Login = "alice"
		return ev
	}
	cases := []struct{ concl, icon, verb string }{
		{"success", "✅", "passed"},
		{"failure", "❌", "failed"},
		{"cancelled", "⚠️", "cancelled"},
		{"timed_out", "ℹ️", "timed_out"},
	}
	for _, c := range cases {
		got := buildRunMessage(mk(c.concl))
		if !strings.HasPrefix(got, c.icon+" CI "+c.verb+"\n📦 o/r") {
			t.Fatalf("conclusion %q: got %q", c.concl, got)
		}
		for _, want := range []string{"🔢 run #7", "🌿 main", "👤 @alice", "🔗 https://x/runs/1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("conclusion %q missing %q in: %q", c.concl, want, got)
			}
		}
	}
}

func TestBuildRunMessage_Enriched(t *testing.T) {
	ev := &workflowRunEvent{}
	ev.Action = "completed"
	ev.WorkflowRun.Name = "deploy"
	ev.WorkflowRun.Conclusion = "success"
	ev.WorkflowRun.HTMLURL = "https://x/runs/9"
	ev.WorkflowRun.HeadBranch = "v1.2.3"
	ev.WorkflowRun.RunNumber = 42
	ev.WorkflowRun.Event = "push"
	ev.WorkflowRun.DisplayTitle = "fix: bump to v1.2.3"
	ev.WorkflowRun.TriggeringActor.Login = "bob"
	ev.WorkflowRun.Actor.Login = "ignored"
	ev.Sender.Login = "ignored2"
	ev.Repository.FullName = "acme/app"

	got := buildRunMessage(ev)
	want := "✅ deploy passed\n📦 acme/app\n\n📝 fix: bump to v1.2.3\n\n🔢 run #42\n🌿 v1.2.3\n⚡ push\n👤 @bob\n\n🔗 https://x/runs/9"
	if got != want {
		t.Fatalf("enriched message mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildRunMessage_DropsTitleEqualToWorkflowName(t *testing.T) {
	ev := &workflowRunEvent{}
	ev.WorkflowRun.Name = "pipeline-3jobs"
	ev.WorkflowRun.Conclusion = "success"
	ev.WorkflowRun.DisplayTitle = "pipeline-3jobs" // GitHub's dispatch/schedule default
	ev.WorkflowRun.HeadBranch = "main"
	ev.WorkflowRun.Event = "workflow_dispatch"
	ev.WorkflowRun.RunNumber = 3
	ev.WorkflowRun.HTMLURL = "https://x/runs/3"
	ev.Repository.FullName = "o/r"
	ev.Sender.Login = "alice"

	got := buildRunMessage(ev)
	want := "✅ pipeline-3jobs passed\n📦 o/r\n\n🔢 run #3\n🌿 main\n⚡ workflow_dispatch\n👤 @alice\n\n🔗 https://x/runs/3"
	if got != want {
		t.Fatalf("redundant title not dropped:\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildRunMessage_TitleFallbackToCommit(t *testing.T) {
	ev := &workflowRunEvent{}
	ev.WorkflowRun.Name = "CI"
	ev.WorkflowRun.Conclusion = "failure"
	ev.WorkflowRun.HeadCommit.Message = "first line of commit\n\nbody paragraph"
	ev.WorkflowRun.RunNumber = 3
	ev.Repository.FullName = "o/r"
	ev.Sender.Login = "carol"

	got := buildRunMessage(ev)
	if !strings.Contains(got, "📝 first line of commit") {
		t.Fatalf("expected commit first line as title, got %q", got)
	}
	if strings.Contains(got, "body paragraph") {
		t.Fatalf("title must be only the first line, got %q", got)
	}
}

type fakeAppOwner struct {
	owner    string
	jwtErr   error
	ownerErr error
}

func (f *fakeAppOwner) AppJWT(_ []byte, _ int64) (string, error) { return "jwt", f.jwtErr }
func (f *fakeAppOwner) AppOwner(_ context.Context, _ string) (string, error) {
	return f.owner, f.ownerErr
}

func newGateHandler(t *testing.T, ao *fakeAppOwner) (*WebhookHandler, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &WebhookHandler{Store: st, GitHub: ao, Log: slog.Default()}, st
}

func repoOf(full string) scheduler.Repository {
	return scheduler.Repository{FullName: full}
}

func TestOwnerAllowed_AppOwnerAlways(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{})
	cfg := &store.AppConfig{OwnerLogin: "acrossoffwest"}
	if !h.ownerAllowed(context.Background(), repoOf("AcrossOffWest/app"), cfg) {
		t.Fatal("app owner must be allowed (case-insensitive)")
	}
}

func TestOwnerAllowed_Listed(t *testing.T) {
	h, st := newGateHandler(t, &fakeAppOwner{})
	_ = st.SaveAccessSettings(context.Background(), &store.AccessSettings{AllowedOwners: "acme, tmgr-dev "})
	cfg := &store.AppConfig{OwnerLogin: "someone"}
	if !h.ownerAllowed(context.Background(), repoOf("TMGR-DEV/backend"), cfg) {
		t.Fatal("listed owner must be allowed (trimmed, case-insensitive)")
	}
}

func TestOwnerAllowed_StrangerDenied(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{})
	cfg := &store.AppConfig{OwnerLogin: "acrossoffwest"}
	if h.ownerAllowed(context.Background(), repoOf("evil/repo"), cfg) {
		t.Fatal("stranger must be denied")
	}
}

func TestOwnerAllowed_LazyResolveAndPersist(t *testing.T) {
	h, st := newGateHandler(t, &fakeAppOwner{owner: "acrossoffwest"})
	ctx := context.Background()
	// Seed an app_config row with an empty owner_login so the lazy resolve
	// has a row to persist into.
	if err := st.SaveAppConfig(ctx, &store.AppConfig{
		AppID: 1, Slug: "s", WebhookSecret: "wsecretwsecret16", PEM: []byte("pem"),
		ClientID: "c", BaseURL: "https://x", OwnerLogin: "",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := st.GetAppConfig(ctx)
	if !h.ownerAllowed(ctx, repoOf("acrossoffwest/x"), cfg) {
		t.Fatal("resolved app owner must be allowed")
	}
	got, _ := st.GetAppConfig(ctx)
	if got == nil || got.OwnerLogin != "acrossoffwest" {
		t.Fatalf("owner_login must be persisted via UpdateAppOwnerLogin, got %+v", got)
	}
}

func TestOwnerAllowed_FailClosed(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{ownerErr: errors.New("boom")})
	cfg := &store.AppConfig{AppID: 1, PEM: []byte("pem"), OwnerLogin: ""} // unresolved
	if h.ownerAllowed(context.Background(), repoOf("acrossoffwest/x"), cfg) {
		t.Fatal("must fail closed when app owner unknown and list empty")
	}
}

// labelsMatch enforces GitHub's cumulative runs-on semantics: a job is
// only accepted if every required label can be satisfied by this pool.
// 'self-hosted' is implicit (GitHub auto-assigns it) so it's always
// satisfiable. Labels matching a dynamic prefix are satisfiable without
// predeclaring every generated value in RUNNER_LABELS. Empty configured =
// serve everything (legacy behavior for direct callers/tests).
func TestLabelsMatch_SupersetSemantics(t *testing.T) {
	makeSet := func(labels []string) map[string]struct{} {
		out := make(map[string]struct{}, len(labels))
		for _, l := range labels {
			out[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
		}
		return out
	}
	cases := []struct {
		name       string
		runsOn     []string
		configured []string
		dynamic    []string
		want       bool
	}{
		// The original failure mode: pool advertises self-hosted, job
		// also wants gpu — must REJECT (current code accepted it,
		// leaving a ghost runner GitHub never bound).
		{"requires-extra-label", []string{"self-hosted", "gpu"}, []string{"self-hosted"}, nil, false},
		{"all-required-present", []string{"self-hosted", "gpu"}, []string{"gpu"}, nil, true},
		{"only-self-hosted", []string{"self-hosted"}, []string{}, nil, true},
		{"empty-configured-serves-everything", []string{"gpu", "linux"}, nil, nil, true},
		{"case-insensitive-match", []string{"Self-Hosted", "GPU"}, []string{"gpu"}, nil, true},
		{"explicit-self-hosted-not-required-in-cfg", []string{"self-hosted"}, []string{"linux"}, nil, true},
		{"job-with-no-labels-trivially-satisfied", nil, []string{"gpu"}, nil, true},
		{"dynamic-prefix-satisfies-label", []string{"self-hosted", "gharp-build-123-1"}, []string{"self-hosted"}, []string{"gharp-"}, true},
		{"dynamic-prefix-case-insensitive-label", []string{"self-hosted", "GHARP-build-123-1"}, []string{"self-hosted"}, []string{"gharp-"}, true},
		{"unknown-dynamic-prefix-rejected", []string{"self-hosted", "other-build-123-1"}, []string{"self-hosted"}, []string{"gharp-"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelsMatch(tc.runsOn, makeSet(tc.configured), tc.dynamic); got != tc.want {
				t.Fatalf("labelsMatch(%v, %v) = %v, want %v", tc.runsOn, tc.configured, got, tc.want)
			}
		})
	}
}
