// Package metrics renders run results in the Prometheus text exposition format.
// The format is small and stable, so it is written by hand to keep the service
// free of third-party dependencies.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
	"github.com/jammiguelyfish/llm-evalwatch/internal/store"
)

// Render writes metrics for the latest run of every series found in runs.
func Render(w io.Writer, runs []runner.Run) {
	latest, counts := store.Latest(runs)
	sort.Slice(latest, func(i, j int) bool { return latest[i].Key() < latest[j].Key() })

	family(w, "evalwatch_pass_rate", "gauge", "Fraction of cases that passed in the latest run.", latest,
		func(r runner.Run) []sample { return []sample{{"", r.PassRate()}} })

	family(w, "evalwatch_cases", "gauge", "Cases in the latest run by outcome.", latest,
		func(r runner.Run) []sample {
			failed := len(r.Results) - r.Passed() - r.Errored()
			return []sample{
				{`status="passed"`, float64(r.Passed())},
				{`status="failed"`, float64(failed)},
				{`status="error"`, float64(r.Errored())},
			}
		})

	family(w, "evalwatch_latency_seconds", "gauge", "Case latency quantiles in the latest run.", latest,
		func(r runner.Run) []sample {
			return []sample{
				{`quantile="0.5"`, r.LatencyQuantile(0.5) / 1000},
				{`quantile="0.95"`, r.LatencyQuantile(0.95) / 1000},
			}
		})

	family(w, "evalwatch_case_passed", "gauge", "1 if the case passed in the latest run, else 0.", latest,
		func(r runner.Run) []sample {
			var s []sample
			for _, c := range r.Results {
				v := 0.0
				if c.Passed {
					v = 1
				}
				s = append(s, sample{fmt.Sprintf(`case="%s"`, escape(c.ID)), v})
			}
			return s
		})

	family(w, "evalwatch_last_run_timestamp_seconds", "gauge", "Unix time the latest run started.", latest,
		func(r runner.Run) []sample { return []sample{{"", float64(r.StartedAt.Unix())}} })

	// counter semantics: derived from the stored history, so it survives restarts
	family(w, "evalwatch_runs_total", "counter", "Runs recorded for this suite and model.", latest,
		func(r runner.Run) []sample { return []sample{{"", float64(counts[r.Key()])}} })
}

type sample struct {
	extraLabels string // e.g. `quantile="0.5"`, appended to the series labels
	value       float64
}

func family(w io.Writer, name, typ, help string, runs []runner.Run, f func(runner.Run) []sample) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	for _, r := range runs {
		labels := fmt.Sprintf(`suite="%s",provider="%s",model="%s"`,
			escape(r.Suite), escape(r.Provider), escape(r.Model))
		for _, s := range f(r) {
			l := labels
			if s.extraLabels != "" {
				l += "," + s.extraLabels
			}
			// round away float noise such as 0.21728530000000001
			fmt.Fprintf(w, "%s{%s} %g\n", name, l, math.Round(s.value*1e6)/1e6)
		}
	}
}

// escape quotes a label value as the exposition format requires.
func escape(v string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(v)
}
