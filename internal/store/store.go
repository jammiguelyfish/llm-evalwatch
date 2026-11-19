// Package store keeps finished runs in an append-only JSON Lines file.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/jammiguelyfish/llm-evalwatch/internal/runner"
)

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
}

// Open returns a store backed by dir/runs.jsonl, creating dir if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "runs.jsonl")}, nil
}

// Append writes one run as a single line.
func (s *Store) Append(r runner.Run) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// All returns every stored run, oldest first. A line that does not parse, such
// as one cut short by a crash mid-write, is skipped so it cannot make the whole
// history unreadable.
func (s *Store) All() ([]runner.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var runs []runner.Run
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var r runner.Run
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			runs = append(runs, r)
		}
	}
	return runs, sc.Err()
}

// Previous returns the most recent stored run in the same series as r that
// started before it, or false if there is none.
func (s *Store) Previous(r runner.Run) (runner.Run, bool, error) {
	runs, err := s.All()
	if err != nil {
		return runner.Run{}, false, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Key() == r.Key() && runs[i].StartedAt.Before(r.StartedAt) {
			return runs[i], true, nil
		}
	}
	return runner.Run{}, false, nil
}

// Latest returns the newest run of every series, plus how many runs each
// series has in total.
func Latest(runs []runner.Run) (latest []runner.Run, counts map[string]int) {
	counts = map[string]int{}
	byKey := map[string]int{}
	for _, r := range runs {
		counts[r.Key()]++
		if i, ok := byKey[r.Key()]; ok {
			if r.StartedAt.After(latest[i].StartedAt) {
				latest[i] = r
			}
			continue
		}
		byKey[r.Key()] = len(latest)
		latest = append(latest, r)
	}
	return latest, counts
}
