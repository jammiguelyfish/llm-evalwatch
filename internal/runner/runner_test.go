package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/provider"
	"github.com/jammiguelyfish/llm-evalwatch/internal/suite"
)

// fake answers from a map keyed by prompt and records what it was asked.
type fake struct {
	answers  map[string]string
	fail     map[string]bool
	lastSys  atomic.Value
	inflight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fake) Name() string  { return "fake" }
func (f *fake) Model() string { return "m1" }
func (f *fake) Generate(ctx context.Context, r provider.Request) (provider.Response, error) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		old := f.maxSeen.Load()
		if n <= old || f.maxSeen.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	f.lastSys.Store(r.System)
	if f.fail[r.Prompt] {
		return provider.Response{}, errors.New("boom")
	}
	return provider.Response{Text: f.answers[r.Prompt], CompletionTokens: 3}, nil
}

func testSuite() *suite.Suite {
	eq := func(v string) []suite.Check { return []suite.Check{{Type: suite.Equals, Value: v}} }
	return &suite.Suite{
		Name:   "t",
		System: "suite-system",
		Cases: []suite.Case{
			{ID: "a", Prompt: "pa", Checks: eq("1")},
			{ID: "b", Prompt: "pb", Checks: eq("2")},
			{ID: "c", Prompt: "pc", Checks: eq("3")},
			{ID: "d", Prompt: "pd", Checks: eq("4")},
		},
	}
}

func TestExecuteScoresCasesAndKeepsOrder(t *testing.T) {
	f := &fake{
		answers: map[string]string{"pa": "1", "pb": "wrong", "pc": "3"},
		fail:    map[string]bool{"pd": true},
	}
	run := Execute(context.Background(), testSuite(), f, Options{Concurrency: 4})

	var ids []string
	for _, r := range run.Results {
		ids = append(ids, r.ID)
	}
	if got := ids[0] + ids[1] + ids[2] + ids[3]; got != "abcd" {
		t.Errorf("order = %s", got)
	}
	if run.Passed() != 2 || run.Errored() != 1 || run.PassRate() != 0.5 {
		t.Errorf("passed %d, errored %d, rate %v", run.Passed(), run.Errored(), run.PassRate())
	}
	if run.Results[3].Error != "boom" || run.Results[3].Passed {
		t.Errorf("provider error should fail the case: %+v", run.Results[3])
	}
	if run.Results[1].Checks[0].Detail == "" {
		t.Error("a failed check should carry its reason")
	}
	if run.Provider != "fake" || run.Model != "m1" || run.Suite != "t" || run.ID == "" {
		t.Errorf("metadata = %+v", run)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	f := &fake{answers: map[string]string{}}
	Execute(context.Background(), testSuite(), f, Options{Concurrency: 2})
	if f.maxSeen.Load() > 2 {
		t.Errorf("saw %d requests in flight, limit is 2", f.maxSeen.Load())
	}
	f2 := &fake{answers: map[string]string{}}
	Execute(context.Background(), testSuite(), f2, Options{}) // default is 1
	if f2.maxSeen.Load() != 1 {
		t.Errorf("default concurrency should be 1, saw %d", f2.maxSeen.Load())
	}
}

func TestWarmupSendsOneExtraUnrecordedRequest(t *testing.T) {
	var calls atomic.Int32
	f := &countingFake{calls: &calls}
	run := Execute(context.Background(), testSuite(), f, Options{Warmup: true})
	if calls.Load() != 5 || len(run.Results) != 4 {
		t.Errorf("%d requests, %d results; want 5 requests (4 cases + warm-up) and 4 results", calls.Load(), len(run.Results))
	}
	calls.Store(0)
	Execute(context.Background(), testSuite(), f, Options{})
	if calls.Load() != 4 {
		t.Errorf("without warm-up want 4 requests, got %d", calls.Load())
	}
}

type countingFake struct{ calls *atomic.Int32 }

func (c *countingFake) Name() string  { return "fake" }
func (c *countingFake) Model() string { return "m1" }
func (c *countingFake) Generate(context.Context, provider.Request) (provider.Response, error) {
	c.calls.Add(1)
	return provider.Response{Text: "x"}, nil
}

func TestCaseSystemPromptOverridesSuite(t *testing.T) {
	s := testSuite()
	s.Cases = s.Cases[:1]
	s.Cases[0].System = "case-system"
	f := &fake{answers: map[string]string{}}
	Execute(context.Background(), s, f, Options{})
	if f.lastSys.Load() != "case-system" {
		t.Errorf("system = %v", f.lastSys.Load())
	}
}

func TestLatencyQuantile(t *testing.T) {
	r := Run{Results: []CaseResult{
		{LatencyMS: 100}, {LatencyMS: 200}, {LatencyMS: 300}, {LatencyMS: 400}, {LatencyMS: 9999, Error: "x"},
	}}
	if got := r.LatencyQuantile(0.5); got != 200 {
		t.Errorf("p50 = %v", got)
	}
	if got := r.LatencyQuantile(0.95); got != 400 {
		t.Errorf("p95 = %v (errored cases must not count)", got)
	}
	if (Run{}).LatencyQuantile(0.5) != 0 || (Run{}).PassRate() != 0 {
		t.Error("an empty run should report zeros")
	}
}
