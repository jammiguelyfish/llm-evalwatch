package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
)

func run(id, model string, minute int, results ...runner.CaseResult) runner.Run {
	return runner.Run{
		ID: id, Suite: "basics", Provider: "ollama", Model: model,
		StartedAt: time.Date(2026, 11, 1, 12, minute, 0, 0, time.UTC),
		Results:   results,
	}
}

func render(runs []runner.Run) string {
	var b bytes.Buffer
	Render(&b, runs)
	return b.String()
}

func TestRenderUsesLatestRunOfEachSeries(t *testing.T) {
	older := run("r1", "m", 0, runner.CaseResult{ID: "a", Passed: false, LatencyMS: 1000})
	newer := run("r2", "m", 5,
		runner.CaseResult{ID: "a", Passed: true, LatencyMS: 100},
		runner.CaseResult{ID: "b", Passed: false, LatencyMS: 300},
		runner.CaseResult{ID: "c", Passed: false, Error: "boom"},
		runner.CaseResult{ID: "d", Passed: true, LatencyMS: 200},
	)
	out := render([]runner.Run{older, newer})
	lab := `{suite="basics",provider="ollama",model="m"`

	for _, want := range []string{
		"evalwatch_pass_rate" + lab + "} 0.5\n",
		"evalwatch_cases" + lab + `,status="passed"} 2`,
		"evalwatch_cases" + lab + `,status="failed"} 1`,
		"evalwatch_cases" + lab + `,status="error"} 1`,
		"evalwatch_case_passed" + lab + `,case="a"} 1`,
		"evalwatch_case_passed" + lab + `,case="b"} 0`,
		"evalwatch_runs_total" + lab + "} 2\n",
		"evalwatch_latency_seconds" + lab + `,quantile="0.95"} 0.3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "evalwatch_pass_rate{") != 1 {
		t.Errorf("only the latest run of a series should be exported:\n%s", out)
	}
}

func TestRenderHasHelpAndTypeForEachFamily(t *testing.T) {
	out := render([]runner.Run{run("r", "m", 0, runner.CaseResult{ID: "a", Passed: true})})
	for _, name := range []string{
		"evalwatch_pass_rate", "evalwatch_cases", "evalwatch_latency_seconds",
		"evalwatch_case_passed", "evalwatch_last_run_timestamp_seconds", "evalwatch_runs_total",
	} {
		if !strings.Contains(out, "# HELP "+name+" ") || !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("%s lacks HELP/TYPE", name)
		}
	}
	if !strings.Contains(out, "# TYPE evalwatch_runs_total counter") {
		t.Error("runs_total should be a counter")
	}
}

func TestRenderEscapesLabelValues(t *testing.T) {
	out := render([]runner.Run{run("r", `we"ird\model`, 0, runner.CaseResult{ID: "line\nbreak", Passed: true})})
	if !strings.Contains(out, `model="we\"ird\\model"`) {
		t.Errorf("model label not escaped:\n%s", out)
	}
	if !strings.Contains(out, `case="line\nbreak"`) {
		t.Errorf("case label not escaped:\n%s", out)
	}
}

func TestRenderEmptyHistoryIsValid(t *testing.T) {
	out := render(nil)
	if strings.Contains(out, "evalwatch_pass_rate{") {
		t.Errorf("no runs should mean no samples:\n%s", out)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	a := run("r1", "alpha", 0, runner.CaseResult{ID: "x", Passed: true})
	b := run("r2", "beta", 0, runner.CaseResult{ID: "x", Passed: true})
	if render([]runner.Run{a, b}) != render([]runner.Run{b, a}) {
		t.Error("output order should not depend on input order")
	}
}

func TestRenderRoundsFloatNoise(t *testing.T) {
	out := render([]runner.Run{run("r", "m", 0, runner.CaseResult{ID: "a", Passed: true, LatencyMS: 217.2853})})
	if !strings.Contains(out, `quantile="0.5"} 0.217285`+"\n") {
		t.Errorf("latency should be rounded to microseconds:\n%s", out)
	}
}
