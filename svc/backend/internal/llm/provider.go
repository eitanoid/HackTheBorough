package llm

import "context"

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
}

type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	InputTokens  int
	OutputTokens int
}

// Provider is the only provider-specific seam.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
