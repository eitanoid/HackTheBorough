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
	// BackfillRows is the first poll's row target; omitted selects the default.
	BackfillRows *int `json:"backfill_rows"`
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
	case "csv":
		if _, err := ingest.ParseCSVConfig(request.Config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be web, rss, or csv"})
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
	// Omitted or 0 selects the server default, as on the Connect API.
	requested := 0
	if request.BackfillRows != nil {
		requested = *request.BackfillRows
	}
	backfillRows, err := ingest.ResolveBackfillRows(requested, s.BackfillDefault)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	source, err := s.Store.CreateSource(r.Context(), request.Name, request.Kind, request.Config, interval, backfillRows)
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
	if sources == nil {
		sources = []ingest.Source{}
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
	// A pending backfill can page through a site for minutes, longer than an
	// HTTP request should live; PollNow hands it to the ingest loop instead.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	result, err := s.Ingest.PollNow(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, ingest.ErrPollQueued):
		writeJSON(w, http.StatusAccepted, map[string]any{"inserted": 0, "queued": true})
	case errors.Is(err, ingest.ErrPollInProgress):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a poll for this source is already running"})
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	case result.Queued:
		writeJSON(w, http.StatusAccepted, map[string]any{"inserted": 0, "queued": true, "backfill": result.Backfill})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"inserted": result.Inserted})
	}
}
