// Package api holds the Connect service and the remaining HTTP handlers, including SSE.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	v1connect "hacktheburgh/backend/gen/hacktheburgh/v1/hacktheburghv1connect"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/workflows"
)

type Server struct {
	Bus *events.Bus
	LLM *llm.Client // nil disables /api/debug/chat
	// Author generates statistical workflows; nil when the LLM is not
	// configured. The project-create and regenerate handlers call Author.Start.
	Author *workflows.Author
	// DebugLLM enables /api/debug/chat, which spends the API key on any caller.
	DebugLLM bool

	Store  *ingest.Store
	Ingest *ingest.Loop
	// AnalysisDebug enables the /api/debug seed/generate/state endpoints,
	// which write to Pool and spend LLM budget for any caller.
	AnalysisDebug bool
	Pool          *pgxpool.Pool
}

type debugChatReq struct {
	System  string `json:"system"`
	Message string `json:"message"`
	Model   string `json:"model"` // optional override
}

// debugChat passes one chat turn straight through to the configured LLM.
func (s *Server) debugChat(w http.ResponseWriter, r *http.Request) {
	var req debugChatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"message": "...", "system"?: "...", "model"?: "..."}`})
		return
	}
	msgs := []llm.Message{{Role: "user", Content: req.Message}}
	if req.System != "" {
		msgs = append([]llm.Message{{Role: "system", Content: req.System}}, msgs...)
	}
	resp, err := s.LLM.Chat(r.Context(), llm.Author, req.Model, msgs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reply": resp.Content, "input_tokens": resp.InputTokens, "output_tokens": resp.OutputTokens,
	})
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "Hello from the Go backend!"})
	})
	mux.HandleFunc("GET /api/stream", s.stream)
	if s.DebugLLM && s.LLM != nil {
		mux.HandleFunc("POST /api/debug/chat", s.debugChat)
	}
	connectServer := connect.NewServer()
	v1connect.RegisterAnduinServiceHandler(connectServer, &ConnectService{Store: s.Store, Bus: s.Bus})
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(ingest.MaxCSVBytes*4/3+(1<<20)))

	if s.AnalysisDebug && s.Pool != nil {
		s.registerAnalysisDebug(mux)
	}

	// Legacy source REST routes remain temporarily available for existing clients.
	mux.HandleFunc("POST /api/sources", s.createSource)
	mux.HandleFunc("GET /api/sources", s.listSources)
	mux.HandleFunc("GET /api/sources/{id}", s.getSource)
	mux.HandleFunc("POST /api/sources/{id}/poll", s.pollSource)

	// Legacy REST project and replay stubs remain during the Connect migration.
	for _, route := range []string{
		"POST /api/sources/csv",
		"POST /api/projects",
		"GET /api/projects",
		"GET /api/projects/{id}",
		"GET /api/projects/{id}/insights",
		"GET /api/projects/{id}/workflows",
		"GET /api/workflows/{id}/steps",
		"POST /api/projects/{id}/workflows/{kind}/regenerate",
		"POST /api/replay",
	} {
		mux.HandleFunc(route, notImplemented)
	}
	return mux
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// stream serves server-sent events: ids only, clients refetch.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	ch, cancel := s.Bus.Subscribe()
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			fl.Flush()
		}
	}
}
