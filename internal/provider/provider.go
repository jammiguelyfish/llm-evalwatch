// Package provider talks to model back ends. Ollama and any OpenAI-compatible
// chat API (OpenAI, vLLM, LM Studio, llama.cpp server, ...) are supported.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Request is one generation request.
type Request struct {
	System      string
	Prompt      string
	Temperature float64
	MaxTokens   int
}

// Response is the model's reply with token counts when the server reports them.
type Response struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
}

// Provider generates text from a prompt.
type Provider interface {
	Name() string
	Model() string
	Generate(ctx context.Context, req Request) (Response, error)
}

// New builds a provider. kind is "ollama" or "openai"; baseURL may be empty
// to use the kind's default.
func New(kind, baseURL, model, apiKey string) (Provider, error) {
	if model == "" {
		return nil, fmt.Errorf("a model name is required")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	switch strings.ToLower(kind) {
	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &ollama{base: strings.TrimRight(baseURL, "/"), model: model, client: client}, nil
	case "openai":
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return &openAI{base: strings.TrimRight(baseURL, "/"), model: model, key: apiKey, client: client}, nil
	}
	return nil, fmt.Errorf("unknown provider %q (use ollama or openai)", kind)
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func messages(req Request) []message {
	var m []message
	if req.System != "" {
		m = append(m, message{"system", req.System})
	}
	return append(m, message{"user", req.Prompt})
}

// post sends a JSON body and decodes a JSON reply, turning non-2xx statuses
// into errors that carry the start of the server's message.
func post(ctx context.Context, client *http.Client, url, key string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}

type ollama struct {
	base, model string
	client      *http.Client
}

func (o *ollama) Name() string  { return "ollama" }
func (o *ollama) Model() string { return o.model }

func (o *ollama) Generate(ctx context.Context, req Request) (Response, error) {
	opts := map[string]any{"temperature": req.Temperature}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	body := map[string]any{
		"model":    o.model,
		"messages": messages(req),
		"stream":   false,
		"options":  opts,
	}
	var out struct {
		Message         message `json:"message"`
		PromptEvalCount int     `json:"prompt_eval_count"`
		EvalCount       int     `json:"eval_count"`
	}
	if err := post(ctx, o.client, o.base+"/api/chat", "", body, &out); err != nil {
		return Response{}, err
	}
	return Response{Text: out.Message.Content, PromptTokens: out.PromptEvalCount, CompletionTokens: out.EvalCount}, nil
}

type openAI struct {
	base, model, key string
	client           *http.Client
}

func (o *openAI) Name() string  { return "openai" }
func (o *openAI) Model() string { return o.model }

func (o *openAI) Generate(ctx context.Context, req Request) (Response, error) {
	body := map[string]any{
		"model":       o.model,
		"messages":    messages(req),
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	var out struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := post(ctx, o.client, o.base+"/chat/completions", o.key, body, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("response had no choices")
	}
	return Response{
		Text:             out.Choices[0].Message.Content,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
	}, nil
}
