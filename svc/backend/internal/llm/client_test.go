package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type providerFunc func(context.Context, ChatRequest) (ChatResponse, error)

func (f providerFunc) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return f(ctx, req)
}

func TestChatLogsProviderError(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	wantErr := errors.New("llm: http 401: incorrect API key")
	client := &Client{
		Provider: providerFunc(func(context.Context, ChatRequest) (ChatResponse, error) {
			return ChatResponse{}, wantErr
		}),
		Timeout: time.Second,
	}

	_, err := client.Chat(context.Background(), Author, "test-model", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	var entry struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
		Purpose string `json:"purpose"`
		Model   string `json:"model"`
		Attempt int    `json:"attempt"`
	}
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v; log=%s", err, logs.String())
	}
	if entry.Level != "ERROR" || entry.Message != "llm request failed" || entry.Purpose != string(Author) || entry.Model != "test-model" || entry.Attempt != 1 {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
}

type captureProvider struct{ got []ChatRequest }

func (c *captureProvider) Chat(_ context.Context, r ChatRequest) (ChatResponse, error) {
	c.got = append(c.got, r)
	return ChatResponse{Content: `{}`}, nil
}

func TestReasoningEffortIsPerPurpose(t *testing.T) {
	p := &captureProvider{}
	c := &Client{Provider: p, Timeout: time.Second,
		Models:  map[Purpose]string{Investigator: "m", Author: "m"},
		Efforts: map[Purpose]string{Investigator: "none"}}
	if _, err := c.call(context.Background(), Investigator, 1, ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(context.Background(), Author, 1, ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if p.got[0].ReasoningEffort != "none" || p.got[1].ReasoningEffort != "" {
		t.Fatalf("efforts = %q, %q", p.got[0].ReasoningEffort, p.got[1].ReasoningEffort)
	}
}
