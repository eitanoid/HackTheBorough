// Package localpy is a development stand-in for the real Python runner. It
// runs each script in a fresh local `python3 -I` process with a minimal
// environment and a wall-clock timeout.
//
// It is NOT the production runner: it has no memory limit and no sandbox
// beyond a scrubbed environment. It exists so the author can be exercised
// end to end in a dev deployment before the real runner lands, and it is only
// wired in when ANALYSIS_DEBUG=1.
package localpy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"hacktheburgh/backend/internal/pyrunner"
)

// wrapper reads {"code", "input"} from stdin, runs the code with INPUT as a
// global, and writes one JSON object to the real stdout. The script's own
// prints are captured (the first 4 KB are returned). result is encoded with NaN and Infinity rejected, as
// the Runner contract requires.
const wrapper = `
import sys, json, io, traceback
payload = json.load(sys.stdin)
real_stdout = sys.stdout
sys.stdout = io.StringIO()
g = {"INPUT": payload["input"], "__name__": "__script__"}
status = {}
try:
    exec(compile(payload["code"], "<script>", "exec"), g)
    status = {"ok": True, "result": json.loads(json.dumps(g["result"], allow_nan=False))}
except BaseException:
    status = {"ok": False, "error": traceback.format_exc()}
status["stdout"] = sys.stdout.getvalue()[:4096]
real_stdout.write(json.dumps(status))
`

// Runner implements pyrunner.Runner with local subprocesses.
type Runner struct {
	Python string // interpreter; "python3" if empty
}

var _ pyrunner.Runner = (*Runner)(nil)

type reply struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Stdout string          `json:"stdout"`
}

func (r *Runner) Run(ctx context.Context, req pyrunner.Request) (pyrunner.Outcome, error) {
	python := r.Python
	if python == "" {
		python = "python3"
	}
	in, err := json.Marshal(map[string]any{"code": req.Code, "input": req.Input})
	if err != nil {
		return pyrunner.Outcome{}, fmt.Errorf("localpy: encode request: %w", err)
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, python, "-I", "-c", wrapper)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Dir = os.TempDir()
	cmd.Env = scrubbedEnv() // the script must not see the backend's secrets
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	if runCtx.Err() != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return pyrunner.Outcome{TimedOut: true}, nil
		}
		return pyrunner.Outcome{}, ctx.Err() // the caller gave up
	}
	var rep reply
	if jerr := json.Unmarshal(stdout.Bytes(), &rep); jerr != nil {
		// No reply at all: the interpreter is missing or the process died
		// (for example, out of memory). That is the runner failing, not the script.
		return pyrunner.Outcome{}, fmt.Errorf("localpy: no usable reply (%v): %s", firstNonNil(runErr, jerr), tail(stderr.String()))
	}
	if !rep.OK {
		return pyrunner.Outcome{Error: rep.Error, Stdout: rep.Stdout}, nil
	}
	return pyrunner.Outcome{OK: true, Result: rep.Result, Stdout: rep.Stdout}, nil
}

// scrubbedEnv is the smallest environment a Python process needs to start.
func scrubbedEnv() []string {
	env := []string{"PYTHONIOENCODING=utf-8", "PYTHONHASHSEED=0"}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SYSTEMROOT", "SystemDrive", "TEMP", "TMP"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	}
	return env
}

func firstNonNil(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func tail(s string) string {
	if len(s) > 500 {
		return "..." + s[len(s)-500:]
	}
	return s
}
