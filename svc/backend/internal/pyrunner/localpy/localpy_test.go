package localpy

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"hacktheburgh/backend/internal/pyrunner"
)

func python(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no python interpreter on PATH")
	return ""
}

func run(t *testing.T, code string, input any, timeout time.Duration) pyrunner.Outcome {
	t.Helper()
	out, err := (&Runner{Python: python(t)}).Run(context.Background(), pyrunner.Request{Code: code, Input: input, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestResultAndInput(t *testing.T) {
	out := run(t, "print('hello')\nresult = {'n': len(INPUT['xs']), 'sum': sum(INPUT['xs'])}", map[string]any{"xs": []int{1, 2, 3}}, 10*time.Second)
	if !out.OK {
		t.Fatalf("outcome = %+v", out)
	}
	var got map[string]float64
	if err := json.Unmarshal(out.Result, &got); err != nil || got["n"] != 3 || got["sum"] != 6 {
		t.Errorf("result = %s (%v)", out.Result, err)
	}
	if out.Stdout != "hello\n" {
		t.Errorf("stdout = %q; script prints must be captured, not mixed into the reply", out.Stdout)
	}
}

func TestRaisedScript(t *testing.T) {
	out := run(t, "x = 1\n1/0\nresult = {}", nil, 10*time.Second)
	if out.OK || out.TimedOut || !strings.Contains(out.Error, "ZeroDivisionError") || out.Result != nil {
		t.Errorf("outcome = %+v", out)
	}
}

func TestMissingResultIsAScriptFailure(t *testing.T) {
	out := run(t, "x = 1", nil, 10*time.Second)
	if out.OK || !strings.Contains(out.Error, "result") {
		t.Errorf("outcome = %+v", out)
	}
}

func TestNaNIsRejected(t *testing.T) {
	out := run(t, "result = {'p': float('nan')}", nil, 10*time.Second)
	if out.OK || !strings.Contains(out.Error, "not JSON compliant") {
		t.Errorf("NaN must be a script failure, got %+v", out)
	}
}

func TestSyntaxErrorIsAScriptFailure(t *testing.T) {
	out := run(t, "def (:", nil, 10*time.Second)
	if out.OK || !strings.Contains(out.Error, "SyntaxError") {
		t.Errorf("outcome = %+v", out)
	}
}

func TestTimeout(t *testing.T) {
	start := time.Now()
	out := run(t, "while True: pass\nresult = {}", nil, 500*time.Millisecond)
	if !out.TimedOut || out.OK {
		t.Errorf("outcome = %+v", out)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the process was not killed at the timeout")
	}
}

func TestBackendEnvironmentIsHidden(t *testing.T) {
	t.Setenv("LOCALPY_TEST_SECRET", "hunter2")
	out := run(t, "import os\nresult = {'seen': os.environ.get('LOCALPY_TEST_SECRET')}", nil, 10*time.Second)
	if !out.OK || strings.Contains(string(out.Result), "hunter2") {
		t.Errorf("the script saw the backend's environment: %+v", out)
	}
}

func TestLargeResultIsNotTruncated(t *testing.T) {
	out := run(t, "result = {'xs': list(range(300000))}", nil, 30*time.Second)
	if !out.OK {
		t.Fatalf("outcome = %+v", out)
	}
	var got struct{ Xs []int }
	if err := json.Unmarshal(out.Result, &got); err != nil || len(got.Xs) != 300000 {
		t.Errorf("result was truncated or mangled: len=%d err=%v", len(got.Xs), err)
	}
}

func TestMissingInterpreterIsARunnerError(t *testing.T) {
	_, err := (&Runner{Python: "definitely-not-a-python"}).Run(context.Background(), pyrunner.Request{Code: "result = {}", Timeout: time.Second})
	if err == nil {
		t.Error("a missing interpreter is the runner failing, not the script")
	}
}

func TestCallerCancellationIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := (&Runner{Python: python(t)}).Run(ctx, pyrunner.Request{Code: "result = {}", Timeout: time.Minute})
	if err == nil || out.TimedOut {
		t.Errorf("out=%+v err=%v", out, err)
	}
}
