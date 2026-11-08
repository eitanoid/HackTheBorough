package subproc

import (
	_ "embed"
	"encoding/json"
)

// bootstrapSrc is the Python program every job runs first. It applies the
// resource limits, installs the audit hook, runs the model's code and writes
// the reply. See bootstrap.py for the stdin and reply formats.
//
//go:embed bootstrap.py
var bootstrapSrc string

// payload is the job as the bootstrap reads it on stdin.
type payload struct {
	Code   string `json:"code"`
	Input  any    `json:"input"`
	Limits limits `json:"limits"`
	Tmp    string `json:"tmp"`
	Audit  bool   `json:"audit"`
}

// limits are the rlimits the bootstrap applies to itself, in bytes or seconds.
type limits struct {
	Mem       int64 `json:"mem"` // RLIMIT_AS; 0 means unlimited
	CPU       int64 `json:"cpu"`
	NoFile    int64 `json:"nofile"`
	FSize     int64 `json:"fsize"`
	MaxResult int64 `json:"maxresult"`
}

// reply is what the bootstrap writes to the reply descriptor.
type reply struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Stdout string          `json:"stdout"`
}
