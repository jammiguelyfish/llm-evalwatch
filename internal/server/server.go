// Package server exposes run history and Prometheus metrics over HTTP and can
// trigger runs on a schedule or on demand.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/compare"
	"github.com/jammiguelyfish/llm-evalwatch/internal/metrics"
	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
	"github.com/jammiguelyfish/llm-evalwatch/internal/store"
)

// RunFunc executes one run (typically suite + provider already bound).
type RunFunc func(ctx context.Context) runner.Run

// Server serves the API. Create it with New.
type Server struct {
	store      *store.Store
	run        RunFunc
	thresholds compare.Thresholds

	busy sync.Mutex // held while a run is in progress
}

// New returns a Server that records runs produced by run into st.
func New(st *store.Store, run RunFunc, th compare.Thresholds) *Server {
	return &Server{store: st, run: run, thresholds: th}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("POST /api/run", s.triggerRun)
	return mux
}

// Summary is the compact form of a run used in listings.
type Summary struct {
	ID         string    `json:"id"`
	Suite      string    `json:"suite"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	StartedAt  time.Time `json:"started_at"`
	Cases      int       `json:"cases"`
	Passed     int       `json:"passed"`
	PassRate   float64   `json:"pass_rate"`
	P95MS      float64   `json:"p95_ms"`
	DurationMS float64   `json:"duration_ms"`
}

func summarize(r runner.Run) Summary {
	return Summary{
		ID: r.ID, Suite: r.Suite, Provider: r.Provider, Model: r.Model, StartedAt: r.StartedAt,
		Cases: len(r.Results), Passed: r.Passed(), PassRate: r.PassRate(),
		P95MS: r.LatencyQuantile(0.95), DurationMS: r.DurationMS,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	runs, err := s.store.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metrics.Render(w, runs)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	suiteName := r.URL.Query().Get("suite")
	out := []Summary{}
	for i := len(runs) - 1; i >= 0 && len(out) < limit; i-- { // newest first
		if suiteName == "" || runs[i].Suite == suiteName {
			out = append(out, summarize(runs[i]))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	for _, run := range runs {
		if run.ID == id {
			writeJSON(w, http.StatusOK, run)
			return
		}
	}
	http.Error(w, "run not found", http.StatusNotFound)
}

// RunOnce executes and records a run, returning it with its comparison to the
// previous run of the same series (nil if there was none). It returns
// ErrBusy if a run is already in progress.
func (s *Server) RunOnce(ctx context.Context) (runner.Run, *compare.Report, error) {
	if !s.busy.TryLock() {
		return runner.Run{}, nil, ErrBusy
	}
	defer s.busy.Unlock()

	run := s.run(ctx)
	prev, ok, err := s.store.Previous(run)
	if err != nil {
		return run, nil, err
	}
	if err := s.store.Append(run); err != nil {
		return run, nil, err
	}
	if !ok {
		return run, nil, nil
	}
	rep := compare.Compare(prev, run, s.thresholds)
	return run, &rep, nil
}

// ErrBusy is returned when a run is requested while another is in progress.
var ErrBusy = errors.New("a run is already in progress")

func (s *Server) triggerRun(w http.ResponseWriter, r *http.Request) {
	run, rep, err := s.RunOnce(r.Context())
	if errors.Is(err, ErrBusy) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": summarize(run), "comparison": rep})
}

// Schedule runs the suite every interval until ctx is cancelled, starting
// immediately. Failures are logged and do not stop the schedule.
func (s *Server) Schedule(ctx context.Context, every time.Duration) {
	for {
		run, rep, err := s.RunOnce(ctx)
		switch {
		case err != nil:
			log.Printf("scheduled run failed: %v", err)
		case rep != nil && rep.Regressed():
			log.Printf("run %s: pass rate %.0f%% -> %.0f%%, regressions: %v",
				run.ID, rep.PassRateBefore*100, rep.PassRateAfter*100, rep.Regressions)
		default:
			log.Printf("run %s: %d/%d passed", run.ID, run.Passed(), len(run.Results))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
