package compare

import (
	"reflect"
	"testing"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
)

func mk(results ...runner.CaseResult) runner.Run {
	return runner.Run{ID: "r", Results: results}
}

func c(id string, passed bool, ms float64) runner.CaseResult {
	return runner.CaseResult{ID: id, Passed: passed, LatencyMS: ms}
}

func TestRegressionsAndFixes(t *testing.T) {
	prev := mk(c("a", true, 100), c("b", false, 100), c("c", true, 100))
	cur := mk(c("a", false, 100), c("b", true, 100), c("c", true, 100))
	rep := Compare(prev, cur, DefaultThresholds)

	if !reflect.DeepEqual(rep.Regressions, []string{"a"}) || !reflect.DeepEqual(rep.Fixes, []string{"b"}) {
		t.Errorf("regressions %v, fixes %v", rep.Regressions, rep.Fixes)
	}
	if !rep.Regressed() {
		t.Error("a newly failing case is a regression")
	}
	if rep.PassRateBefore < 0.66 || rep.PassRateBefore > 0.67 || rep.PassRateAfter < 0.66 || rep.PassRateAfter > 0.67 {
		t.Errorf("rates %v -> %v", rep.PassRateBefore, rep.PassRateAfter)
	}
}

func TestCasesInOnlyOneRunAreIgnored(t *testing.T) {
	prev := mk(c("old", true, 100))
	cur := mk(c("new", false, 100))
	if rep := Compare(prev, cur, DefaultThresholds); rep.Regressed() {
		t.Errorf("adding or removing a case is not a regression: %+v", rep)
	}
}

func TestLatencyNeedsBothRelativeAndAbsoluteGrowth(t *testing.T) {
	th := Thresholds{LatencyRatio: 0.5, LatencyMinMS: 250}
	cases := []struct {
		name          string
		before, after float64
		want          bool
	}{
		{"big and slow", 1000, 2000, true},
		{"relative only (tiny absolute)", 100, 200, false},
		{"absolute only (small relative)", 10000, 10300, false},
		{"faster", 1000, 500, false},
		{"exactly at the ratio is not over it", 1000, 1500, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := Compare(mk(c("a", true, tc.before)), mk(c("a", true, tc.after)), th)
			if rep.LatencyRegressed != tc.want {
				t.Errorf("LatencyRegressed = %v, want %v", rep.LatencyRegressed, tc.want)
			}
			if rep.Regressed() != tc.want {
				t.Errorf("Regressed() = %v, want %v", rep.Regressed(), tc.want)
			}
		})
	}
}

func TestNoRegressionWhenIdentical(t *testing.T) {
	r := mk(c("a", true, 100), c("b", false, 100))
	if rep := Compare(r, r, DefaultThresholds); rep.Regressed() || len(rep.Fixes) != 0 {
		t.Errorf("identical runs: %+v", rep)
	}
}
