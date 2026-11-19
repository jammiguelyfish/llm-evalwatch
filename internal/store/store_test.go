package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func run(id, model string, minutes int) runner.Run {
	return runner.Run{
		ID: id, Suite: "s", Provider: "ollama", Model: model,
		StartedAt: t0.Add(time.Duration(minutes) * time.Minute),
		Results:   []runner.CaseResult{{ID: "c", Passed: true}},
	}
}

func TestAppendAndAllRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nested", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := st.All(); err != nil || len(runs) != 0 {
		t.Fatalf("empty store: %v, %v", runs, err)
	}
	st.Append(run("r1", "m", 0))
	st.Append(run("r2", "m", 1))
	runs, err := st.All()
	if err != nil || len(runs) != 2 || runs[0].ID != "r1" || runs[1].ID != "r2" {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
}

func TestAllSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	st.Append(run("r1", "m", 0))
	f, _ := os.OpenFile(filepath.Join(dir, "runs.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{\"id\":\"cut off mid-wri\n\n")
	f.Close()
	st.Append(run("r2", "m", 1))

	runs, err := st.All()
	if err != nil || len(runs) != 2 {
		t.Fatalf("a torn line must not hide the rest: %+v, %v", runs, err)
	}
}

func TestPreviousIsSameSeriesAndEarlier(t *testing.T) {
	st, _ := Open(t.TempDir())
	st.Append(run("a1", "model-a", 0))
	st.Append(run("b1", "model-b", 1))
	st.Append(run("a2", "model-a", 2))

	prev, ok, _ := st.Previous(run("a3", "model-a", 3))
	if !ok || prev.ID != "a2" {
		t.Errorf("previous of a3 = %v %v, want a2", prev.ID, ok)
	}
	prev, ok, _ = st.Previous(run("a2", "model-a", 2))
	if !ok || prev.ID != "a1" {
		t.Errorf("previous of a2 = %v %v, want a1 (not itself)", prev.ID, ok)
	}
	if _, ok, _ := st.Previous(run("c1", "model-c", 5)); ok {
		t.Error("a new series has no previous run")
	}
}

func TestLatestKeepsNewestPerSeriesAndCounts(t *testing.T) {
	runs := []runner.Run{run("a1", "a", 0), run("b1", "b", 1), run("a2", "a", 2)}
	latest, counts := Latest(runs)
	if len(latest) != 2 {
		t.Fatalf("latest = %d series", len(latest))
	}
	for _, r := range latest {
		if r.Model == "a" && r.ID != "a2" {
			t.Errorf("latest of a = %s", r.ID)
		}
	}
	if counts[runs[0].Key()] != 2 || counts[runs[1].Key()] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestConcurrentAppendsStayIntact(t *testing.T) {
	st, _ := Open(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st.Append(run("r"+string(rune('A'+i%26))+string(rune('a'+i/26)), "m", i))
		}(i)
	}
	wg.Wait()
	runs, err := st.All()
	if err != nil || len(runs) != 40 {
		t.Errorf("got %d runs, err %v; lines must not interleave", len(runs), err)
	}
}
