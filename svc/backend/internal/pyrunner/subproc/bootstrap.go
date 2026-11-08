package subproc

import (
	_ "embed"
	"encoding/json"
)

//go:embed bootstrap.py
var bootstrapSrc string

type payload struct {
	Code   string `json:"code"`
	Input  any    `json:"input"`
	Limits limits `json:"limits"`
	Tmp    string `json:"tmp"`
	Audit  bool   `json:"audit"`
}

type limits struct {
	Mem       int64 `json:"mem"` // RLIMIT_AS; 0 means unlimited
	CPU       int64 `json:"cpu"`
	NoFile    int64 `json:"nofile"`
	FSize     int64 `json:"fsize"`
	MaxResult int64 `json:"maxresult"`
}

type reply struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Stdout string          `json:"stdout"`
}
