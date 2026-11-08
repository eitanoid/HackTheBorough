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
// using structured outputs (response_format json_schema, strict). Requests
// with API set to APIResponses go to the Responses API instead.
type OpenAI struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func (o *OpenAI) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.API == APIResponses {
		return o.responses(ctx, req)
	}
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
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
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
	raw, err := o.post(ctx, "/chat/completions", body)
	if err != nil {
		return ChatResponse{}, err
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

// post sends body as JSON to BaseURL+path and returns the body of a 200
// reply; any other status is an error carrying the start of the body.
func (o *OpenAI) post(ctx context.Context, path string, body map[string]any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
