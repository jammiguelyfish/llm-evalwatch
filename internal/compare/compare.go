// Package compare finds regressions between two runs of the same suite.
package compare

import (
	"sort"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
)

// Thresholds decide when a latency change counts as a regression. Latency is
// noisy, so a change must exceed both a relative and an absolute bound.
type Thresholds struct {
	LatencyRatio float64 // p95 may grow by this fraction (0.5 = 50%)
	LatencyMinMS float64 // ... and by at least this many milliseconds
}

// DefaultThresholds is a p95 that is 50% slower and at least 250 ms slower.
var DefaultThresholds = Thresholds{LatencyRatio: 0.5, LatencyMinMS: 250}

// Report describes how a run differs from the one before it.
type Report struct {
	PreviousID       string   `json:"previous_id"`
	PassRateBefore   float64  `json:"pass_rate_before"`
	PassRateAfter    float64  `json:"pass_rate_after"`
	Regressions      []string `json:"regressions"` // passed before, fail now
	Fixes            []string `json:"fixes"`       // failed before, pass now
	P95BeforeMS      float64  `json:"p95_before_ms"`
	P95AfterMS       float64  `json:"p95_after_ms"`
	LatencyRegressed bool     `json:"latency_regressed"`
}

// Compare diffs cur against prev. Cases present in only one run are ignored,
// so adding or removing cases does not register as a regression.
func Compare(prev, cur runner.Run, th Thresholds) Report {
	before := map[string]bool{}
	for _, c := range prev.Results {
		before[c.ID] = c.Passed
	}
	rep := Report{
		PreviousID:     prev.ID,
		PassRateBefore: prev.PassRate(),
		PassRateAfter:  cur.PassRate(),
		P95BeforeMS:    prev.LatencyQuantile(0.95),
		P95AfterMS:     cur.LatencyQuantile(0.95),
	}
	for _, c := range cur.Results {
		was, ok := before[c.ID]
		switch {
		case !ok:
		case was && !c.Passed:
			rep.Regressions = append(rep.Regressions, c.ID)
		case !was && c.Passed:
			rep.Fixes = append(rep.Fixes, c.ID)
		}
	}
	sort.Strings(rep.Regressions)
	sort.Strings(rep.Fixes)
	grew := rep.P95AfterMS - rep.P95BeforeMS
	rep.LatencyRegressed = rep.P95BeforeMS > 0 &&
		grew >= th.LatencyMinMS && grew > rep.P95BeforeMS*th.LatencyRatio
	return rep
}

// Regressed reports whether the run got worse: a case that used to pass now
// fails, or latency regressed.
func (r Report) Regressed() bool {
	return len(r.Regressions) > 0 || r.LatencyRegressed
}
