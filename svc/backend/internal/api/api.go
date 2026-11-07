// Package api holds HTTP handlers, SSE and replay.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"hacktheburgh/backend/internal/events"
)

type Server struct {
	Bus *events.Bus
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "Hello from the Go backend!"})
	})
	mux.HandleFunc("GET /api/stream", s.stream)

	// Stubs: 501 until workstream A implements them.
	for _, route := range []string{
		"POST /api/sources",
		"POST /api/sources/csv",
		"GET /api/sources",
		"GET /api/sources/{id}",
		"POST /api/projects",
		"GET /api/projects",
		"GET /api/projects/{id}",
		"GET /api/projects/{id}/insights",
		"POST /api/projects/{id}/workflow/regenerate",
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
