// Package subproc is the Python runner: it executes model-written code in a
// fresh restricted child process of the backend, one process per job.
//
// Isolation is deliberately light: a scrubbed
// environment, its own process group, rlimits, a private temp directory, a
// run-as-nobody drop when the backend is root, and a soft audit hook inside
// the child. It limits accidents and casual misuse of model-written code; it
// is not a hard sandbox.
package subproc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"hacktheburgh/backend/internal/pyrunner"
)

const (
	defaultTimeout = 60 * time.Second
	cpuGrace       = 5 // seconds of CPU allowed beyond the wall-clock timeout
	replyOverhead  = 256 << 10
	stderrKeep     = 4 << 10
)

type Config struct {
	Python        string // interpreter; "python3" if empty
	MaxConcurrent int    // jobs at once; 2 if zero
	MemMB         int    // RLIMIT_AS per job; 1024 if zero, negative for none
	MaxResultMB   int    // largest result; 16 if zero
	DisableAudit  bool   // skip the in-child audit hook (tests, or if it breaks a script)
	Logger        *slog.Logger
}

type Runner struct {
	cfg    Config
	sem    chan struct{}
	drop   *cred
	jobSeq atomic.Uint64
	log    *slog.Logger
}

var _ pyrunner.Runner = (*Runner)(nil)

func New(cfg Config) *Runner {
	if cfg.Python == "" {
		cfg.Python = "python3"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.MemMB == 0 {
		cfg.MemMB = 1024
	}
	if cfg.MaxResultMB <= 0 {
		cfg.MaxResultMB = 16
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runner{cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrent), drop: dropTarget(), log: log}
}

// SelfCheck runs a trivial job that imports pandas and numpy under the real
// limits. It returns their versions; an error means the runner is unusable.
func (r *Runner) SelfCheck(ctx context.Context) (map[string]string, error) {
	out, err := r.Run(ctx, pyrunner.Request{
		Code: "import sys, numpy, pandas\n" +
			"result = {'python': sys.version.split()[0], 'numpy': numpy.__version__, 'pandas': pandas.__version__}\n",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("self-check script failed: %s", out.Error)
	}
	var v map[string]string
	if err := json.Unmarshal(out.Result, &v); err != nil {
		return nil, fmt.Errorf("self-check reply: %w", err)
	}
	return v, nil
}

func (r *Runner) Run(ctx context.Context, req pyrunner.Request) (pyrunner.Outcome, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return pyrunner.Outcome{}, ctx.Err()
	}

	id := r.jobSeq.Add(1)
	start := time.Now()
	out, kind, inBytes, rssKB, err := r.run(ctx, req)
	if err != nil {
		kind = "runner_error"
	}
	sum := sha256.Sum256([]byte(req.Code))
	r.log.Info("python job",
		"job", id, "code_sha256", hex.EncodeToString(sum[:8]), "input_bytes", inBytes,
		"duration_ms", time.Since(start).Milliseconds(), "outcome", kind, "peak_rss_kb", rssKB)
	return out, err
}

func (r *Runner) run(ctx context.Context, req pyrunner.Request) (out pyrunner.Outcome, kind string, inBytes int, rssKB int64, err error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxResult := int64(r.cfg.MaxResultMB) << 20

	dir, err := os.MkdirTemp("", "pyrun-")
	if err != nil {
		return out, "", 0, 0, fmt.Errorf("subproc: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	lim := limits{
		CPU:       int64(timeout/time.Second) + cpuGrace,
		NoFile:    256,
		FSize:     64 << 20,
		MaxResult: maxResult,
	}
	if r.cfg.MemMB > 0 {
		lim.Mem = int64(r.cfg.MemMB) << 20
	}
	in, err := json.Marshal(payload{Code: req.Code, Input: req.Input, Limits: lim, Tmp: dir, Audit: !r.cfg.DisableAudit})
	if err != nil {
		return out, "", 0, 0, fmt.Errorf("subproc: encode request: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	replyR, replyW, err := os.Pipe()
	if err != nil {
		return out, "", len(in), 0, fmt.Errorf("subproc: pipe: %w", err)
	}
	defer replyR.Close()

	// The reply travels on its own descriptor so the script's prints cannot
	// corrupt it. Windows cannot pass extra descriptors, so the dev fallback
	// uses the child's stdout.
	replyFD := "3"
	cmd := exec.CommandContext(runCtx, r.cfg.Python, "-s", "-P", "-c", bootstrapSrc)
	if runtime.GOOS == "windows" {
		replyFD = "1"
		cmd.Stdout = replyW
	} else {
		cmd.ExtraFiles = []*os.File{replyW}
	}
	cmd.Args = append(cmd.Args, replyFD)
	cmd.Dir = dir
	cmd.Env = childEnv(dir)
	cmd.Stdin = bytes.NewReader(in)
	stderr := &tailBuffer{max: stderrKeep}
	cmd.Stderr = stderr
	cmd.Cancel = func() error { killGroup(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	if err := prepare(cmd, dir, r.drop); err != nil {
		replyW.Close()
		return out, "", len(in), 0, fmt.Errorf("subproc: prepare: %w", err)
	}

	if err := cmd.Start(); err != nil {
		replyW.Close()
		return out, "", len(in), 0, fmt.Errorf("subproc: start %s: %w", r.cfg.Python, err)
	}
	replyW.Close() // the child holds the only write end now, so EOF means it is gone

	type collected struct {
		data []byte
		over bool
	}
	got := make(chan collected, 1)
	go func() {
		limit := maxResult + replyOverhead
		data, _ := io.ReadAll(io.LimitReader(replyR, limit+1))
		over := int64(len(data)) > limit
		if over {
			io.Copy(io.Discard, replyR) // keep the child from blocking on a full pipe
		}
		got <- collected{data, over}
	}()

	_ = cmd.Wait()
	killGroup(cmd) // anything the script left behind
	c := <-got
	rssKB = peakRSSKB(cmd.ProcessState)

	if runCtx.Err() != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return pyrunner.Outcome{TimedOut: true}, "timeout", len(in), rssKB, nil
		}
		return out, "", len(in), rssKB, ctx.Err() // the caller gave up
	}

	if c.over {
		return pyrunner.Outcome{Error: fmt.Sprintf("result exceeds %d MB", r.cfg.MaxResultMB)}, "too_large", len(in), rssKB, nil
	}
	var rep reply
	if len(c.data) > 0 && json.Unmarshal(c.data, &rep) == nil {
		if !rep.OK {
			return pyrunner.Outcome{Error: rep.Error, Stdout: rep.Stdout}, "raised", len(in), rssKB, nil
		}
		return pyrunner.Outcome{OK: true, Result: rep.Result, Stdout: rep.Stdout}, "ok", len(in), rssKB, nil
	}

	// No usable reply. A child killed by a signal (usually the kernel's
	// out-of-memory kill) is the script's doing; anything else is the runner.
	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return pyrunner.Outcome{Error: fmt.Sprintf("the script was killed (%v), likely over the memory limit", ws.Signal())}, "killed", len(in), rssKB, nil
		}
	}
	return out, "", len(in), rssKB, fmt.Errorf("subproc: no usable reply (exit %s): %s", exitString(cmd.ProcessState), stderr.String())
}

func childEnv(dir string) []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"TZ=UTC",
		"LANG=C.UTF-8",
		"PYTHONHASHSEED=0",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"OPENBLAS_NUM_THREADS=1",
		"OMP_NUM_THREADS=1",
		"MKL_NUM_THREADS=1",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "TEMP="+dir, "TMP="+dir)
		for _, k := range []string{"SYSTEMROOT", "SystemDrive"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	}
	return env
}

func exitString(ps *os.ProcessState) string {
	if ps == nil {
		return "unknown"
	}
	return strconv.Itoa(ps.ExitCode())
}

type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
