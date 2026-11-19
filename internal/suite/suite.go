// Package suite defines evaluation suites (prompts plus deterministic checks)
// and evaluates model output against them.
package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Check types. Every check is deterministic, so a failing case is a real
// change in model behaviour and not judge noise.
const (
	Contains    = "contains"     // output contains value (case-insensitive)
	NotContains = "not_contains" // output does not contain value
	Equals      = "equals"       // trimmed output equals value (case-insensitive)
	AnyOf       = "any_of"       // output contains at least one of values
	Regex       = "regex"        // output matches the regular expression in value
	MaxWords    = "max_words"    // output has at most n words
	JSONValid   = "json_valid"   // output is valid JSON (a code fence is ignored)
)

// Check is one assertion about a model's output.
type Check struct {
	Type   string   `json:"type"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	N      int      `json:"n,omitempty"`
}

// Case is one prompt with the checks its output must pass.
type Case struct {
	ID     string   `json:"id"`
	Prompt string   `json:"prompt"`
	System string   `json:"system,omitempty"` // overrides the suite system prompt
	Checks []Check  `json:"checks"`
	Tags   []string `json:"tags,omitempty"`
}

// Suite is a named list of cases.
type Suite struct {
	Name   string `json:"name"`
	System string `json:"system,omitempty"`
	Cases  []Case `json:"cases"`
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	Check  Check  `json:"check"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Load reads and validates a suite file.
func Load(path string) (*Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Validate reports the first problem that would make the suite unusable.
func (s *Suite) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite needs a name")
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("suite %q has no cases", s.Name)
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if c.ID == "" {
			return fmt.Errorf("a case has no id")
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if strings.TrimSpace(c.Prompt) == "" {
			return fmt.Errorf("case %q has no prompt", c.ID)
		}
		if len(c.Checks) == 0 {
			return fmt.Errorf("case %q has no checks", c.ID)
		}
		for _, ch := range c.Checks {
			if err := ch.validate(); err != nil {
				return fmt.Errorf("case %q: %w", c.ID, err)
			}
		}
	}
	return nil
}

func (c Check) validate() error {
	switch c.Type {
	case Contains, NotContains, Equals:
		if c.Value == "" {
			return fmt.Errorf("%s check needs a value", c.Type)
		}
	case AnyOf:
		if len(c.Values) == 0 {
			return fmt.Errorf("any_of check needs values")
		}
	case Regex:
		if _, err := regexp.Compile(c.Value); err != nil {
			return fmt.Errorf("bad regex %q: %w", c.Value, err)
		}
	case MaxWords:
		if c.N <= 0 {
			return fmt.Errorf("max_words check needs n > 0")
		}
	case JSONValid:
	default:
		return fmt.Errorf("unknown check type %q", c.Type)
	}
	return nil
}

// Evaluate runs every check against the output.
func Evaluate(output string, checks []Check) []CheckResult {
	out := make([]CheckResult, len(checks))
	for i, c := range checks {
		passed, detail := c.eval(output)
		out[i] = CheckResult{Check: c, Passed: passed, Detail: detail}
	}
	return out
}

// AllPassed reports whether every check passed.
func AllPassed(results []CheckResult) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return true
}

func (c Check) eval(output string) (bool, string) {
	lower := strings.ToLower(output)
	switch c.Type {
	case Contains:
		if strings.Contains(lower, strings.ToLower(c.Value)) {
			return true, ""
		}
		return false, fmt.Sprintf("missing %q", c.Value)
	case NotContains:
		if strings.Contains(lower, strings.ToLower(c.Value)) {
			return false, fmt.Sprintf("contains forbidden %q", c.Value)
		}
		return true, ""
	case Equals:
		got := strings.ToLower(strings.TrimSpace(output))
		if got == strings.ToLower(strings.TrimSpace(c.Value)) {
			return true, ""
		}
		return false, fmt.Sprintf("expected %q", c.Value)
	case AnyOf:
		for _, v := range c.Values {
			if strings.Contains(lower, strings.ToLower(v)) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("none of %q found", c.Values)
	case Regex:
		// the pattern was validated at load time
		if regexp.MustCompile(c.Value).MatchString(output) {
			return true, ""
		}
		return false, fmt.Sprintf("does not match /%s/", c.Value)
	case MaxWords:
		if n := len(strings.Fields(output)); n > c.N {
			return false, fmt.Sprintf("%d words, limit %d", n, c.N)
		}
		return true, ""
	case JSONValid:
		if json.Valid([]byte(stripFence(output))) {
			return true, ""
		}
		return false, "not valid JSON"
	}
	return false, "unknown check type"
}

// stripFence removes a surrounding Markdown code fence, which chat models
// often add around JSON even when told not to.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.Index(s, "\n"); nl >= 0 {
		s = s[nl+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
