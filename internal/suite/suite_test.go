package suite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pass(t *testing.T, out string, c Check) bool {
	t.Helper()
	r := Evaluate(out, []Check{c})
	return r[0].Passed
}

func TestChecks(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		check Check
		want  bool
	}{
		{"contains ignores case", "The capital is PARIS.", Check{Type: Contains, Value: "paris"}, true},
		{"contains miss", "Lyon", Check{Type: Contains, Value: "paris"}, false},
		{"not_contains ok", "Mango", Check{Type: NotContains, Value: "apple"}, true},
		{"not_contains leak", "It is Pineapple", Check{Type: NotContains, Value: "pineapple"}, false},
		{"equals trims and ignores case", "  Wednesday \n", Check{Type: Equals, Value: "wednesday"}, true},
		{"equals rejects extra words", "It is Wednesday", Check{Type: Equals, Value: "wednesday"}, false},
		{"any_of hit", "I can't know that", Check{Type: AnyOf, Values: []string{"don't know", "can't"}}, true},
		{"any_of miss", "Rex", Check{Type: AnyOf, Values: []string{"don't know", "can't"}}, false},
		{"regex match", "1, 2, 3", Check{Type: Regex, Value: "^[[:digit:]], [[:digit:]], [[:digit:]]$"}, true},
		{"regex miss", "one", Check{Type: Regex, Value: "^[[:digit:]]$"}, false},
		{"max_words within", "one two three", Check{Type: MaxWords, N: 3}, true},
		{"max_words over", "one two three four", Check{Type: MaxWords, N: 3}, false},
		{"json valid", `{"a": 1}`, Check{Type: JSONValid}, true},
		{"json in fence", "```json\n{\"a\": 1}\n```", Check{Type: JSONValid}, true},
		{"json invalid", `{"a": }`, Check{Type: JSONValid}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pass(t, tt.out, tt.check); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllPassed(t *testing.T) {
	r := Evaluate("hi", []Check{{Type: Contains, Value: "hi"}, {Type: Contains, Value: "bye"}})
	if AllPassed(r) {
		t.Error("one check failed, AllPassed should be false")
	}
	if r[1].Detail == "" {
		t.Error("a failed check should say why")
	}
}

func valid() Suite {
	return Suite{Name: "s", Cases: []Case{{ID: "a", Prompt: "p", Checks: []Check{{Type: JSONValid}}}}}
}

func TestValidate(t *testing.T) {
	s := valid()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid suite rejected: %v", err)
	}
	mutations := map[string]func(*Suite){
		"no name":      func(s *Suite) { s.Name = "" },
		"no cases":     func(s *Suite) { s.Cases = nil },
		"no id":        func(s *Suite) { s.Cases[0].ID = "" },
		"no prompt":    func(s *Suite) { s.Cases[0].Prompt = " " },
		"no checks":    func(s *Suite) { s.Cases[0].Checks = nil },
		"unknown type": func(s *Suite) { s.Cases[0].Checks = []Check{{Type: "vibes"}} },
		"bad regex":    func(s *Suite) { s.Cases[0].Checks = []Check{{Type: Regex, Value: "("}} },
		"empty value":  func(s *Suite) { s.Cases[0].Checks = []Check{{Type: Contains}} },
		"zero words":   func(s *Suite) { s.Cases[0].Checks = []Check{{Type: MaxWords}} },
		"duplicate id": func(s *Suite) { s.Cases = append(s.Cases, s.Cases[0]) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := valid()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	body := `{"name":"s","cases":[{"id":"a","prompt":"p","chekcs":[]}]}`
	os.WriteFile(path, []byte(body), 0o644)
	if _, err := Load(path); err == nil {
		t.Error("a misspelt field should be an error, not silently ignored")
	}
}

// The bundled example suite must always load, or the README's commands break.
func TestBundledSuiteLoads(t *testing.T) {
	s, err := Load(filepath.Join("..", "..", "suites", "basics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cases) < 10 || !strings.EqualFold(s.Name, "basics") {
		t.Errorf("unexpected suite: %q with %d cases", s.Name, len(s.Cases))
	}
}
