package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAI talks to the Chat Completions API (or any compatible endpoint)
// using structured outputs (response_format json_schema, strict).
type OpenAI struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func (o *OpenAI) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		jm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				tcs[i] = map[string]any{"id": tc.ID, "type": "function",
					"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments}}
			}
			jm["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			jm["tool_call_id"] = m.ToolCallID
		}
		msgs = append(msgs, jm)
	}
	body := map[string]any{"model": req.Model, "messages": msgs}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters}}
		}
		body["tools"] = tools
	}
	if req.Schema != nil {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": req.Schema.Name, "strict": true, "schema": req.Schema.Schema}}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return ChatResponse{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("llm: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Refusal   string `json:"refusal"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ChatResponse{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("llm: response has no choices")
	}
	m := out.Choices[0].Message
	if m.Refusal != "" {
		return ChatResponse{}, fmt.Errorf("llm: model refused: %s", truncate(m.Refusal, 200))
	}
	res := ChatResponse{Content: m.Content, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	for _, tc := range m.ToolCalls {
		res.ToolCalls = append(res.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
