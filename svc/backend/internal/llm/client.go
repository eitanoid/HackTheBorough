// Package llm is the only place LLM calls are made: structured outputs with a
// contract struct as the schema, 60s timeout, one retry, and a JSONL call log.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"hacktheburgh/backend/internal/config"
)

type Purpose string

const (
	Profiler     Purpose = "profiler"
	Author       Purpose = "author"
	Investigator Purpose = "investigator"
	Reviewer     Purpose = "reviewer"
	Narrator     Purpose = "narrator"
)

const maxToolOutput = 4096 // bytes of tool output shown to the model

type Client struct {
	Provider Provider
	Models   map[Purpose]string
	Timeout  time.Duration // per model call
	MaxIter  int           // tool-runner iterations
	Log      *CallLog
}

// New builds an OpenAI client from config.
func New(cfg config.Config) (*Client, error) {
	c := &Client{
		Models: map[Purpose]string{
			Profiler: cfg.ModelProfiler, Author: cfg.ModelAuthor, Investigator: cfg.ModelInvestigator,
			Reviewer: cfg.ModelReviewer, Narrator: cfg.ModelNarrator,
		},
		Timeout: cfg.LLMTimeout,
		MaxIter: cfg.MaxInvestigatorIt,
	}
	if cfg.LLMLogPath != "" {
		l, err := OpenCallLog(cfg.LLMLogPath, cfg.LLMLogPrompts)
		if err != nil {
			return nil, err
		}
		c.Log = l
	}
	if cfg.LLMAPIKey == "" {
		return nil, errors.New("llm: OPENAPI_KEY is not set")
	}
	if cfg.LLMBaseURL == "" {
		return nil, errors.New("llm: LLM_BASE_URL is not set")
	}
	c.Provider = &OpenAI{APIKey: cfg.LLMAPIKey, BaseURL: cfg.LLMBaseURL, HTTP: &http.Client{}}
	return c, nil
}

type Request struct {
	Purpose Purpose
	System  string
	User    string
}

// Generate asks the model for a T, unmarshals the reply and runs validate
// (nil to skip). It makes at most two attempts; a retry after a validation
// failure shows the model its error.
func Generate[T any](ctx context.Context, c *Client, r Request, validate func(T) error) (T, error) {
	name, schema := SchemaFor[T]()
	msgs := []Message{{Role: "system", Content: r.System}, {Role: "user", Content: r.User}}
	return finish(ctx, c, r.Purpose, msgs, nil, &SchemaSpec{Name: name, Schema: schema}, validate)
}

// Chat sends free-form messages and returns the plain-text reply. It is for
// debugging only; real features use Generate or RunTools. An empty model
// falls back to the purpose's model.
func (c *Client) Chat(ctx context.Context, p Purpose, model string, msgs []Message) (ChatResponse, error) {
	if model == "" {
		model = c.Models[p]
	}
	return c.call(ctx, p, 1, ChatRequest{Model: model, Messages: msgs})
}

// Tool is a read-only capability the model may call. Handler gets the raw
// JSON arguments and returns anything JSON-marshalable. The handler records
// its own full output; the model sees at most 4 KB.
type Tool struct {
	Name        string
	Description string
	InputSchema any
	Handler     func(ctx context.Context, input json.RawMessage) (any, error)
}

// RunTools is the investigator loop: the model may call tools for up to
// MaxIter rounds, then must answer with a T. Handler errors go back to the
// model as tool results instead of aborting the run.
func RunTools[T any](ctx context.Context, c *Client, r Request, tools []Tool, validate func(T) error) (T, error) {
	name, schema := SchemaFor[T]()
	msgs := []Message{{Role: "system", Content: r.System}, {Role: "user", Content: r.User}}
	specs := make([]ToolSpec, len(tools))
	byName := map[string]Tool{}
	for i, t := range tools {
		specs[i] = ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}
		byName[t.Name] = t
	}
	var zero T
	for i := 0; i < c.MaxIter; i++ {
		resp, err := c.call(ctx, r.Purpose, 1, ChatRequest{Model: c.Models[r.Purpose], Messages: msgs, Tools: specs,
			Schema: &SchemaSpec{Name: name, Schema: schema}})
		if err != nil {
			return zero, err
		}
		if len(resp.ToolCalls) == 0 {
			return finish(ctx, c, r.Purpose, msgs, &resp.Content, &SchemaSpec{Name: name, Schema: schema}, validate)
		}
		msgs = append(msgs, Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, Content: runTool(ctx, byName, tc)})
		}
	}
	// Iteration cap reached: force a final answer without tools.
	msgs = append(msgs, Message{Role: "user", Content: "Tool budget exhausted. Give your final answer now using what you have."})
	return finish(ctx, c, r.Purpose, msgs, nil, &SchemaSpec{Name: name, Schema: schema}, validate)
}

func runTool(ctx context.Context, byName map[string]Tool, tc ToolCall) string {
	t, ok := byName[tc.Name]
	if !ok {
		return `{"error":"unknown tool"}`
	}
	out, err := t.Handler(ctx, json.RawMessage(tc.Arguments))
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"error":"unserialisable tool output"}`
	}
	return truncate(string(b), maxToolOutput)
}

// finish obtains (or takes, if first is set) a reply, parses and validates it,
// retrying once with the error shown to the model.
func finish[T any](ctx context.Context, c *Client, p Purpose, msgs []Message, first *string, schema *SchemaSpec, validate func(T) error) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		var content string
		if first != nil && attempt == 1 {
			content = *first
		} else {
			resp, err := c.call(ctx, p, attempt, ChatRequest{Model: c.Models[p], Messages: msgs, Schema: schema})
			if err != nil {
				lastErr = err
				continue
			}
			content = resp.Content
		}
		var v T
		err := json.Unmarshal([]byte(content), &v)
		if err == nil && validate != nil {
			err = validate(v)
		}
		c.Log.write(logEntry{TS: time.Now(), Purpose: p, Model: c.Models[p], Attempt: attempt, ValidationOK: err == nil, Error: errStr(err)})
		if err == nil {
			return v, nil
		}
		lastErr = fmt.Errorf("invalid %s output: %w", p, err)
		msgs = append(msgs, Message{Role: "assistant", Content: content},
			Message{Role: "user", Content: "That reply was rejected: " + err.Error() + ". Reply again with a corrected JSON object."})
	}
	return zero, lastErr
}

// call makes one timed model call and logs it.
func (c *Client) call(ctx context.Context, p Purpose, attempt int, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		return ChatResponse{}, fmt.Errorf("llm: no model configured for %s", p)
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	start := time.Now()
	resp, err := c.Provider.Chat(ctx, req)
	if err != nil {
		slog.Error("llm request failed", "purpose", p, "model", req.Model, "attempt", attempt, "err", err)
	}
	e := logEntry{TS: start, Purpose: p, Model: req.Model, Attempt: attempt, LatencyMS: time.Since(start).Milliseconds(),
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, Error: errStr(err), Prompt: req.Messages, Reply: resp.Content}
	c.Log.write(e)
	return resp, err
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
