package llm

import (
	"context"
	"encoding/json"
)

// API names which endpoint a request goes to. The default is chat completions.
const (
	APIChat      = "chat"
	APIResponses = "responses"
)

// ToolCall is a model request to run one tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON
}

// Message is one chat turn. Role is system, user, assistant or tool.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall // assistant turns
	ToolCallID string     // tool turns
	// Raw is the provider's own items for an assistant turn (the Responses
	// API's reasoning and function_call items). The provider replays them
	// instead of Content and ToolCalls so the model keeps its reasoning across
	// tool calls. Never logged: it holds encrypted reasoning.
	Raw []json.RawMessage `json:"-"`
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  any // JSON schema
}

// SchemaSpec asks for the final reply to match a JSON schema.
type SchemaSpec struct {
	Name   string
	Schema map[string]any
}

type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
	Schema   *SchemaSpec
	// API is APIChat (or empty) or APIResponses. Reasoning models only accept
	// function tools on the Responses API.
	API string
	// ReasoningEffort is sent as reasoning_effort (chat) or reasoning.effort
	// (responses) when not empty. Models without reasoning reject it, so it is
	// off by default.
	ReasoningEffort string
}

type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	InputTokens  int
	OutputTokens int
	// Raw holds the provider's output items to put back on the assistant
	// Message; nil on chat completions.
	Raw []json.RawMessage
}

// Provider is the only provider-specific seam.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
