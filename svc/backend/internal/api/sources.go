package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"hacktheburgh/backend/internal/ingest"
)

type createSourceRequest struct {
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	PollIntervalS *int            `json:"poll_interval_s"`
	Config        json.RawMessage `json:"config"`
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request createSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Kind = strings.ToLower(strings.TrimSpace(request.Kind))
	if request.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if len(request.Config) == 0 || !json.Valid(request.Config) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "config must be valid JSON"})
		return
	}
	switch request.Kind {
	case "web":
		if _, err := ingest.ParseWebConfig(request.Config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	case "rss":
		if _, err := ingest.ParseRSSConfig(request.Config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be web or rss"})
		return
	}

	interval := request.PollIntervalS
	if interval == nil {
		defaultInterval := 300
		interval = &defaultInterval
	}
	if *interval < 1 || *interval > 86400 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "poll_interval_s must be from 1 to 86400"})
		return
	}
	source, err := s.Store.CreateSource(r.Context(), request.Name, request.Kind, request.Config, interval)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, source)
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
		return
	}
	sources, err := s.Store.ListSources(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

func (s *Server) getSource(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
		return
	}
	source, err := s.Store.Source(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "source not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) pollSource(w http.ResponseWriter, r *http.Request) {
	if s.Ingest == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ingestion unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	inserted, err := s.Ingest.PollOnce(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inserted": inserted})
}
