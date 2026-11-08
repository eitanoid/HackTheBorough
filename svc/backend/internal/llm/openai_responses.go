package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// responses sends req to the Responses API. It is stateless (store=false):
// the model's reasoning comes back encrypted in ChatResponse.Raw and is sent
// again with the next turn, so reasoning survives across tool calls without
// the provider keeping the conversation.
func (o *OpenAI) responses(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var input []any
	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			if len(m.Raw) > 0 {
				for _, it := range m.Raw {
					input = append(input, it)
				}
				continue
			}
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Arguments})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case "system":
			input = append(input, map[string]any{"role": "developer", "content": m.Content})
		default:
			input = append(input, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	body := map[string]any{
		"model": req.Model, "input": input,
		"store": false, "include": []string{"reasoning.encrypted_content"},
	}
	if req.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters}
		}
		body["tools"] = tools
	}
	if req.Schema != nil {
		body["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": req.Schema.Name, "strict": true, "schema": req.Schema.Schema}}
	}
	raw, err := o.post(ctx, "/responses", body)
	if err != nil {
		return ChatResponse{}, err
	}

	var out struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ChatResponse{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if out.Error != nil {
		return ChatResponse{}, fmt.Errorf("llm: %s", truncate(out.Error.Message, 300))
	}
	res := ChatResponse{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, Raw: out.Output}
	for _, itemRaw := range out.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return ChatResponse{}, fmt.Errorf("llm: decode output item: %w", err)
		}
		switch item.Type {
		case "function_call":
			res.ToolCalls = append(res.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					res.Content += c.Text
				case "refusal":
					return ChatResponse{}, fmt.Errorf("llm: model refused: %s", truncate(c.Refusal, 200))
				}
			}
		}
	}
	if out.Status == "incomplete" && res.Content == "" && len(res.ToolCalls) == 0 {
		reason := "unknown"
		if out.IncompleteDetails != nil {
			reason = out.IncompleteDetails.Reason
		}
		return ChatResponse{}, fmt.Errorf("llm: response incomplete (%s)", reason)
	}
	return res, nil
}
