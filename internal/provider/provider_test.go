package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewValidates(t *testing.T) {
	if _, err := New("ollama", "", "", ""); err == nil {
		t.Error("missing model should be an error")
	}
	if _, err := New("nope", "", "m", ""); err == nil {
		t.Error("unknown provider should be an error")
	}
	p, err := New("OLLAMA", "", "m", "")
	if err != nil || p.Name() != "ollama" || p.Model() != "m" {
		t.Errorf("got %v, %v", p, err)
	}
}

func TestOllamaGenerate(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"message":{"role":"assistant","content":"43"},"prompt_eval_count":12,"eval_count":2}`))
	}))
	defer srv.Close()

	p, _ := New("ollama", srv.URL+"/", "qwen", "")
	resp, err := p.Generate(context.Background(), Request{System: "be brief", Prompt: "17+26?", MaxTokens: 16})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "43" || resp.PromptTokens != 12 || resp.CompletionTokens != 2 {
		t.Errorf("response = %+v", resp)
	}
	if got["model"] != "qwen" || got["stream"] != false {
		t.Errorf("request body = %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %v", msgs)
	}
	if opts := got["options"].(map[string]any); opts["num_predict"] != float64(16) {
		t.Errorf("options = %v", opts)
	}
}

func TestOpenAIGenerateSendsKeyAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("path %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	p, _ := New("openai", srv.URL, "gpt", "sk-test")
	resp, err := p.Generate(context.Background(), Request{Prompt: "hello"})
	if err != nil || resp.Text != "hi" || resp.PromptTokens != 5 || resp.CompletionTokens != 1 {
		t.Errorf("resp = %+v, err = %v", resp, err)
	}
}

func TestErrorsCarryStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := New("ollama", srv.URL, "missing", "")
	_, err := p.Generate(context.Background(), Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAIEmptyChoicesIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	p, _ := New("openai", srv.URL, "m", "")
	if _, err := p.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Error("expected an error")
	}
}
