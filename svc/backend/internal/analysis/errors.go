package analysis

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNoRunner = errors.New("analysis: no Python runner is configured")

// ScriptError is a failure of the author's script, as opposed to the runner
// failing. Callers use it to decide whether the author gets a retry.
type ScriptError struct {
	Kind      string // raised | timeout | invalid_result
	Message   string
	Traceback string
}

func (e *ScriptError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = lastLine(e.Traceback) // the exception itself, without the stack
	}
	if msg != "" {
		return fmt.Sprintf("script %s: %s", e.Kind, msg)
	}
	return fmt.Sprintf("script %s", e.Kind)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

const maxTracebackChars = 3000

// ForAuthor is what the author's retry sees: the error only, never results.
func (e *ScriptError) ForAuthor() string {
	switch e.Kind {
	case "raised":
		tb := e.Traceback
		if len(tb) > maxTracebackChars {
			tb = "...\n" + tb[len(tb)-maxTracebackChars:] // the end holds the actual error
		}
		return "The script raised an exception:\n" + tb
	case "timeout":
		return "The script timed out: " + e.Message + ". Make it cheaper, for example by analysing fewer series or lags."
	default:
		return "The script's result was rejected: " + e.Message
	}
}
