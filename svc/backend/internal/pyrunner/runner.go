// Package pyrunner defines how the backend executes model-written Python.
package pyrunner

import (
	"context"
	"encoding/json"
	"time"
)

type Request struct {
	Code    string        // Python source
	Input   any           // JSON-marshalable; the code sees it as the global dict INPUT
	Timeout time.Duration // wall-clock limit for the code
}

// Outcome is what the code did. A script that raised or timed out is an
// Outcome with OK false, not an error.
type Outcome struct {
	OK       bool
	Result   json.RawMessage // the code's global `result`, JSON-encoded, untruncated; nil unless OK
	Stdout   string          // may be truncated (4 KB)
	Error    string          // traceback when the code raised
	TimedOut bool
}

// Runner executes model-written Python in a child process with a 1 GB memory
// limit, with pandas and numpy available (no scipy or statsmodels). It returns an
// error only when the runner itself fails (unavailable, crashed, bad reply),
// never because the code failed.
//
// An implementation must guarantee:
//   - INPUT is a global dict in the code's namespace.
//   - result is encoded with NaN and Infinity rejected (Python allow_nan=False);
//     an encoding failure is reported as OK false with the error.
//   - Result is never truncated; up to 16 MB must be supported.
//   - The code cannot see the backend's environment or secrets.
type Runner interface {
	Run(ctx context.Context, r Request) (Outcome, error)
}
