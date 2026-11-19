// Package runner executes a suite against a provider and records the result.
package runner

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/provider"
	"github.com/jammiguelyfish/llm-evalwatch/internal/suite"
)

// CaseResult is the outcome of one case. A provider error counts as a failure
// and is recorded in Error so it can be told apart from a wrong answer.
type CaseResult struct {
	ID               string              `json:"id"`
	Tags             []string            `json:"tags,omitempty"`
	Passed           bool                `json:"passed"`
	Output           string              `json:"output"`
	Error            string              `json:"error,omitempty"`
	LatencyMS        float64             `json:"latency_ms"`
	PromptTokens     int                 `json:"prompt_tokens,omitempty"`
	CompletionTokens int                 `json:"completion_tokens,omitempty"`
	Checks           []suite.CheckResult `json:"checks,omitempty"`
}

// Run is one execution of a suite against one model.
type Run struct {
	ID         string       `json:"id"`
	Suite      string       `json:"suite"`
	Provider   string       `json:"provider"`
	Model      string       `json:"model"`
	StartedAt  time.Time    `json:"started_at"`
	DurationMS float64      `json:"duration_ms"`
	Results    []CaseResult `json:"results"`
}

// Options tunes a run.
type Options struct {
	Concurrency int     // parallel requests; default 1
	Temperature float64 // default 0, so runs are comparable
	MaxTokens   int
	// Warmup sends one throwaway request first, so a model that has to be
	// loaded into memory does not turn the first case into a latency outlier.
	Warmup bool
}

// Execute runs every case and returns the run. Results keep the suite's order.
// If ctx is cancelled, cases that had not started are recorded as errors.
func Execute(ctx context.Context, s *suite.Suite, p provider.Provider, opt Options) Run {
	if opt.Concurrency < 1 {
		opt.Concurrency = 1
	}
	start := time.Now().UTC()
	run := Run{
		ID:        fmt.Sprintf("%s-%s", start.Format("20060102T150405Z"), s.Name),
		Suite:     s.Name,
		Provider:  p.Name(),
		Model:     p.Model(),
		StartedAt: start,
		Results:   make([]CaseResult, len(s.Cases)),
	}

	if opt.Warmup && len(s.Cases) > 0 {
		runCase(ctx, s, s.Cases[0], p, opt)
		start = time.Now().UTC()
		run.StartedAt = start
		run.ID = fmt.Sprintf("%s-%s", start.Format("20060102T150405Z"), s.Name)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, opt.Concurrency)
	for i := range s.Cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			run.Results[i] = runCase(ctx, s, s.Cases[i], p, opt)
		}(i)
	}
	wg.Wait()
	run.DurationMS = float64(time.Since(start)) / float64(time.Millisecond)
	return run
}

func runCase(ctx context.Context, s *suite.Suite, c suite.Case, p provider.Provider, opt Options) CaseResult {
	res := CaseResult{ID: c.ID, Tags: c.Tags}
	system := s.System
	if c.System != "" {
		system = c.System
	}
	t0 := time.Now()
	resp, err := p.Generate(ctx, provider.Request{
		System: system, Prompt: c.Prompt, Temperature: opt.Temperature, MaxTokens: opt.MaxTokens,
	})
	res.LatencyMS = float64(time.Since(t0)) / float64(time.Millisecond)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Output = resp.Text
	res.PromptTokens, res.CompletionTokens = resp.PromptTokens, resp.CompletionTokens
	res.Checks = suite.Evaluate(resp.Text, c.Checks)
	res.Passed = suite.AllPassed(res.Checks)
	return res
}

// Passed is the number of cases that passed.
func (r Run) Passed() int {
	n := 0
	for _, c := range r.Results {
		if c.Passed {
			n++
		}
	}
	return n
}

// Errored is the number of cases that failed because the provider errored.
func (r Run) Errored() int {
	n := 0
	for _, c := range r.Results {
		if c.Error != "" {
			n++
		}
	}
	return n
}

// PassRate is the fraction of cases that passed, 0 for an empty run.
func (r Run) PassRate() float64 {
	if len(r.Results) == 0 {
		return 0
	}
	return float64(r.Passed()) / float64(len(r.Results))
}

// LatencyQuantile returns the q-quantile (0..1) of case latency in
// milliseconds by nearest rank, ignoring cases that errored.
func (r Run) LatencyQuantile(q float64) float64 {
	var v []float64
	for _, c := range r.Results {
		if c.Error == "" {
			v = append(v, c.LatencyMS)
		}
	}
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	rank := int(q*float64(len(v)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(v) {
		rank = len(v)
	}
	return v[rank-1]
}

// Key identifies a series of comparable runs.
func (r Run) Key() string { return r.Suite + "|" + r.Provider + "|" + r.Model }
