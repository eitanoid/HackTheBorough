package llm

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// CallLog appends one JSON line per model call. A nil *CallLog discards.
type CallLog struct {
	mu      sync.Mutex
	f       *os.File
	Prompts bool // include prompt and reply text
}

func OpenCallLog(path string, prompts bool) (*CallLog, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &CallLog{f: f, Prompts: prompts}, nil
}

type logEntry struct {
	TS           time.Time `json:"ts"`
	Purpose      Purpose   `json:"purpose"`
	Model        string    `json:"model"`
	Attempt      int       `json:"attempt"`
	LatencyMS    int64     `json:"latency_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	ValidationOK bool      `json:"validation_ok"`
	Error        string    `json:"error,omitempty"`
	Prompt       []Message `json:"prompt,omitempty"`
	Reply        string    `json:"reply,omitempty"`
}

func (l *CallLog) write(e logEntry) {
	if l == nil || l.f == nil {
		return
	}
	if !l.Prompts {
		e.Prompt, e.Reply = nil, ""
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.Write(append(b, '\n'))
}

func (l *CallLog) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}
