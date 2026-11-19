package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/compare"
	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
	"github.com/jammiguelyfish/llm-evalwatch/internal/store"
)

// newServer returns a server whose runs are produced by results, one per call.
func newServer(t *testing.T, results ...[]runner.CaseResult) (*Server, *int32) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls int32
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	run := func(context.Context) runner.Run {
		i := int(atomic.AddInt32(&calls, 1)) - 1
		return runner.Run{
			ID: "run-" + string(rune('a'+i)), Suite: "basics", Provider: "fake", Model: "m",
			StartedAt: clock.Add(time.Duration(i) * time.Minute), Results: results[i%len(results)],
		}
	}
	return New(st, run, compare.DefaultThresholds), &calls
}

func ok(id string) runner.CaseResult  { return runner.CaseResult{ID: id, Passed: true, LatencyMS: 100} }
func bad(id string) runner.CaseResult { return runner.CaseResult{ID: id, LatencyMS: 100} }
func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	s, _ := newServer(t, []runner.CaseResult{ok("a")})
	if rec := get(s.Handler(), "/healthz"); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "ok") {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestRunOnceRecordsAndComparesWithPrevious(t *testing.T) {
	s, _ := newServer(t, []runner.CaseResult{ok("a"), ok("b")}, []runner.CaseResult{ok("a"), bad("b")})

	_, rep, err := s.RunOnce(context.Background())
	if err != nil || rep != nil {
		t.Fatalf("first run: report %v, err %v (nothing to compare with)", rep, err)
	}
	_, rep, err = s.RunOnce(context.Background())
	if err != nil || rep == nil {
		t.Fatalf("second run: %v, %v", rep, err)
	}
	if len(rep.Regressions) != 1 || rep.Regressions[0] != "b" || !rep.Regressed() {
		t.Errorf("report = %+v", rep)
	}
}

func TestTriggerRunEndpointAndRunsListing(t *testing.T) {
	s, _ := newServer(t, []runner.CaseResult{ok("a")})
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/run", nil))
	if rec.Code != 200 {
		t.Fatalf("POST /api/run = %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Run Summary `json:"run"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Run.ID != "run-a" || resp.Run.PassRate != 1 {
		t.Errorf("run = %+v", resp.Run)
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/run", nil))
	var list []Summary
	json.Unmarshal(get(h, "/api/runs").Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != "run-b" {
		t.Errorf("listing should be newest first with 2 runs: %+v", list)
	}
	json.Unmarshal(get(h, "/api/runs?limit=1").Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("limit ignored: %d", len(list))
	}
	json.Unmarshal(get(h, "/api/runs?suite=other").Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("suite filter ignored: %d", len(list))
	}
}

func TestGetRunAndNotFound(t *testing.T) {
	s, _ := newServer(t, []runner.CaseResult{ok("a")})
	s.RunOnce(context.Background())
	h := s.Handler()

	rec := get(h, "/api/runs/run-a")
	var full runner.Run
	json.Unmarshal(rec.Body.Bytes(), &full)
	if rec.Code != 200 || len(full.Results) != 1 {
		t.Errorf("get run = %d %+v", rec.Code, full)
	}
	if rec := get(h, "/api/runs/nope"); rec.Code != 404 {
		t.Errorf("unknown run = %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s, _ := newServer(t, []runner.CaseResult{ok("a"), bad("b")})
	s.RunOnce(context.Background())
	rec := get(s.Handler(), "/metrics")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `evalwatch_pass_rate{suite="basics",provider="fake",model="m"} 0.5`) {
		t.Errorf("metrics body:\n%s", rec.Body.String())
	}
}

func TestOnlyOneRunAtATime(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	s := New(st, func(context.Context) runner.Run {
		close(started)
		<-release
		return runner.Run{ID: "slow", Suite: "s", Provider: "p", Model: "m", StartedAt: time.Now()}
	}, compare.DefaultThresholds)

	done := make(chan struct{})
	go func() { s.RunOnce(context.Background()); close(done) }()
	<-started

	if _, _, err := s.RunOnce(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("second concurrent run err = %v, want ErrBusy", err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/run", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("POST while busy = %d, want 409", rec.Code)
	}
	close(release)
	<-done
}

func TestScheduleRunsImmediatelyThenRepeatsUntilCancelled(t *testing.T) {
	s, calls := newServer(t, []runner.CaseResult{ok("a")})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { s.Schedule(ctx, 20*time.Millisecond); close(finished) }()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d scheduled runs", atomic.LoadInt32(calls))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Schedule did not stop after cancel")
	}
}
