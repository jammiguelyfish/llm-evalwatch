// Command evalwatch runs LLM evaluation suites, tracks results over time and
// serves them as Prometheus metrics.
//
//	evalwatch run   -suite suites/basics.json -model qwen2.5:1.5b-instruct
//	evalwatch serve -suite suites/basics.json -model qwen2.5:1.5b-instruct -every 15m
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/compare"
	"github.com/jammiguelyfish/llm-evalwatch/internal/provider"
	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
	"github.com/jammiguelyfish/llm-evalwatch/internal/server"
	"github.com/jammiguelyfish/llm-evalwatch/internal/store"
	"github.com/jammiguelyfish/llm-evalwatch/internal/suite"
)

const usage = `evalwatch - evaluate LLMs and watch for regressions

Usage:
  evalwatch run   [flags]   run a suite once, record it, compare with the previous run
  evalwatch serve [flags]   serve /metrics and /api, optionally re-running on a schedule

Run "evalwatch <command> -h" for flags.
`

// target holds the flags that describe what to evaluate and with what.
type target struct {
	suitePath   string
	provider    string
	url         string
	model       string
	apiKeyEnv   string
	concurrency int
	temperature float64
	maxTokens   int
	warmup      bool
	dataDir     string
	latencyPct  float64
	latencyMS   float64
}

func (t *target) register(fs *flag.FlagSet) {
	fs.StringVar(&t.suitePath, "suite", "suites/basics.json", "suite file")
	fs.StringVar(&t.provider, "provider", "ollama", "ollama or openai (any OpenAI-compatible API)")
	fs.StringVar(&t.url, "url", "", "provider base URL (default depends on provider)")
	fs.StringVar(&t.model, "model", "", "model name (required)")
	fs.StringVar(&t.apiKeyEnv, "api-key-env", "OPENAI_API_KEY", "environment variable holding the API key")
	fs.IntVar(&t.concurrency, "concurrency", 1, "parallel requests")
	fs.Float64Var(&t.temperature, "temperature", 0, "sampling temperature; keep 0 so runs are comparable")
	fs.IntVar(&t.maxTokens, "max-tokens", 256, "max tokens per answer")
	fs.BoolVar(&t.warmup, "warmup", true, "send one unrecorded request first so model loading does not skew latency")
	fs.StringVar(&t.dataDir, "data", "data", "directory for run history")
	fs.Float64Var(&t.latencyPct, "latency-regression-pct", 50, "p95 latency growth (%) that counts as a regression")
	fs.Float64Var(&t.latencyMS, "latency-regression-ms", 250, "...and the minimum growth in ms")
}

func (t *target) thresholds() compare.Thresholds {
	return compare.Thresholds{LatencyRatio: t.latencyPct / 100, LatencyMinMS: t.latencyMS}
}

// build loads the suite and provider and returns a function that runs once.
func (t *target) build() (server.RunFunc, *suite.Suite, error) {
	s, err := suite.Load(t.suitePath)
	if err != nil {
		return nil, nil, err
	}
	p, err := provider.New(t.provider, t.url, t.model, os.Getenv(t.apiKeyEnv))
	if err != nil {
		return nil, nil, err
	}
	opt := runner.Options{Concurrency: t.concurrency, Temperature: t.temperature, MaxTokens: t.maxTokens, Warmup: t.warmup}
	return func(ctx context.Context) runner.Run { return runner.Execute(ctx, s, p, opt) }, s, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	var gate gateError
	switch {
	case err == nil:
	case errors.As(err, &gate):
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	case errors.Is(err, flag.ErrHelp):
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

// gateError means the run completed but did not meet the requested bar, which
// CI should treat differently from a crash.
type gateError string

func (e gateError) Error() string { return string(e) }

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var t target
	t.register(fs)
	minRate := fs.Float64("min-pass-rate", 0, "exit 1 if the pass rate (0-1) is below this")
	failOnRegression := fs.Bool("fail-on-regression", false, "exit 1 if a case that passed last run now fails, or latency regressed")
	asJSON := fs.Bool("json", false, "print the full run as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	run, s, err := t.build()
	if err != nil {
		return err
	}
	st, err := store.Open(t.dataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(st, run, t.thresholds())
	fmt.Fprintf(os.Stderr, "running %d cases of %q against %s/%s ...\n", len(s.Cases), s.Name, t.provider, t.model)
	result, rep, err := srv.RunOnce(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"run": result, "comparison": rep})
	}
	printReport(result, rep)

	if result.PassRate() < *minRate {
		return gateError(fmt.Sprintf("pass rate %.0f%% is below the required %.0f%%", result.PassRate()*100, *minRate*100))
	}
	if *failOnRegression && rep != nil && rep.Regressed() {
		return gateError("regression against the previous run")
	}
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var t target
	t.register(fs)
	addr := fs.String("addr", ":8080", "listen address")
	every := fs.Duration("every", 0, "re-run the suite on this interval (0 = only via POST /api/run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	run, _, err := t.build()
	if err != nil {
		return err
	}
	st, err := store.Open(t.dataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(st, run, t.thresholds())
	if *every > 0 {
		go srv.Schedule(ctx, *every)
	}
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "listening on %s (metrics at /metrics)\n", *addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func printReport(r runner.Run, rep *compare.Report) {
	fmt.Printf("\n%s  %s/%s\n\n", r.Suite, r.Provider, r.Model)
	for _, c := range r.Results {
		mark := "PASS"
		note := ""
		switch {
		case c.Error != "":
			mark, note = "ERR ", c.Error
		case !c.Passed:
			mark = "FAIL"
			note = firstFailure(c)
		}
		fmt.Printf("  %s  %-22s %6.0f ms  %s\n", mark, c.ID, c.LatencyMS, note)
	}
	fmt.Printf("\n%d/%d passed (%.0f%%)  p50 %.0f ms  p95 %.0f ms  total %.1f s\n",
		r.Passed(), len(r.Results), r.PassRate()*100,
		r.LatencyQuantile(0.5), r.LatencyQuantile(0.95), r.DurationMS/1000)

	if rep == nil {
		fmt.Println("first run of this suite and model: nothing to compare with yet")
		return
	}
	fmt.Printf("vs previous run (%s): pass rate %.0f%% -> %.0f%%, p95 %.0f ms -> %.0f ms\n",
		rep.PreviousID, rep.PassRateBefore*100, rep.PassRateAfter*100, rep.P95BeforeMS, rep.P95AfterMS)
	if len(rep.Regressions) > 0 {
		fmt.Println("  regressions:", strings.Join(rep.Regressions, ", "))
	}
	if len(rep.Fixes) > 0 {
		fmt.Println("  fixed:      ", strings.Join(rep.Fixes, ", "))
	}
	if rep.LatencyRegressed {
		fmt.Println("  latency regressed")
	}
}

func firstFailure(c runner.CaseResult) string {
	for _, ch := range c.Checks {
		if !ch.Passed {
			return ch.Detail
		}
	}
	return ""
}
