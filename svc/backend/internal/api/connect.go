package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "hacktheburgh/backend/gen/hacktheburgh/v1"
	v1connect "hacktheburgh/backend/gen/hacktheburgh/v1/hacktheburghv1connect"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/workflows"
)

// ConnectService implements the browser-facing RPCs. Project creation persists
// the project and its source links, then starts statistical workflow generation
// when an author is configured.
type ConnectService struct {
	Store  *ingest.Store
	Bus    *events.Bus
	Author *workflows.Author
	Runner *workflows.Runner
}

var _ v1connect.AnduinServiceHandler = (*ConnectService)(nil)

func (s *ConnectService) CreateSource(ctx context.Context, req *v1.CreateSourceRequest) (*v1.CreateSourceResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	kind, config, err := sourceConfig(req.GetKind(), req.GetConfig())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	interval := req.GetPollIntervalSeconds()
	if interval == 0 {
		interval = 300
	}
	if interval < 1 || interval > 86400 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "poll_interval_seconds must be from 1 to 86400")
	}
	intervalValue := int(interval)
	source, err := s.Store.CreateSource(ctx, name, kind, config, &intervalValue)
	if err != nil {
		return nil, internalError(err)
	}
	response, err := sourceMessage(source)
	if err != nil {
		return nil, internalError(err)
	}
	return &v1.CreateSourceResponse{Source: response}, nil
}

func (s *ConnectService) UploadCSVSource(ctx context.Context, req *v1.UploadCSVSourceRequest) (*v1.UploadCSVSourceResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	fileName := strings.TrimSpace(req.GetFileName())
	if fileName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "file_name is required")
	}
	data := req.GetCsvData()
	if len(data) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "csv_data is required")
	}
	if len(data) > ingest.MaxCSVBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("csv_data exceeds %d bytes", ingest.MaxCSVBytes))
	}
	records, err := ingest.ParseCSV(data, "", time.Now().UTC())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	source, err := s.Store.CreateUploadedCSV(ctx, name, fileName, records)
	if err != nil {
		return nil, internalError(err)
	}
	response, err := sourceMessage(source)
	if err != nil {
		return nil, internalError(err)
	}
	return &v1.UploadCSVSourceResponse{Source: response}, nil
}

func (s *ConnectService) ListSources(ctx context.Context, _ *v1.ListSourcesRequest) (*v1.ListSourcesResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	sources, err := s.Store.ListSources(ctx)
	if err != nil {
		return nil, internalError(err)
	}
	response := &v1.ListSourcesResponse{Sources: make([]*v1.Source, 0, len(sources))}
	for _, source := range sources {
		message, err := sourceMessage(source)
		if err != nil {
			return nil, internalError(err)
		}
		response.Sources = append(response.Sources, message)
	}
	return response, nil
}

func (s *ConnectService) GetSource(ctx context.Context, req *v1.GetSourceRequest) (*v1.GetSourceResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "id is required")
	}
	source, err := s.Store.Source(ctx, id)
	if err != nil {
		return nil, sourceLookupError(err)
	}
	message, err := sourceMessage(source)
	if err != nil {
		return nil, internalError(err)
	}
	records, err := s.Store.RecentRecords(ctx, id, 20)
	if err != nil {
		return nil, sourceLookupError(err)
	}
	response := &v1.GetSourceResponse{Source: message, RecentRecords: make([]*v1.DataRecord, 0, len(records))}
	for _, record := range records {
		payload, err := structpb.NewStruct(record.Payload)
		if err != nil {
			return nil, internalError(err)
		}
		response.RecentRecords = append(response.RecentRecords, &v1.DataRecord{
			Id: record.ID, ExternalId: record.ExternalID,
			ObservedAt: timestamppb.New(record.ObservedAt), Payload: payload,
		})
	}
	return response, nil
}

func (s *ConnectService) DeleteSource(ctx context.Context, req *v1.DeleteSourceRequest) (*v1.DeleteSourceResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	rawID := strings.TrimSpace(req.GetId())
	id, err := uuid.Parse(rawID)
	if rawID == "" || err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "id must be a UUID")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, internalError(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM data_sources WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
		return nil, sourceLookupError(err)
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id::text
		FROM projects p
		JOIN project_sources ps ON ps.project_id = p.id
		WHERE ps.data_source_id = $1
		ORDER BY p.id
		FOR UPDATE OF p`, id)
	if err != nil {
		return nil, internalError(err)
	}
	var affected []uuid.UUID
	for rows.Next() {
		var projectID uuid.UUID
		if err := rows.Scan(&projectID); err != nil {
			rows.Close()
			return nil, internalError(err)
		}
		affected = append(affected, projectID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, internalError(err)
	}
	rows.Close()

	if len(affected) > 0 {
		message := fmt.Sprintf("Source %q was deleted; workflow results were cleared.", name)
		if _, err := tx.Exec(ctx, `UPDATE projects SET last_error = $2 WHERE id = ANY($1)`, affected, message); err != nil {
			return nil, internalError(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE workflows SET is_active = false WHERE project_id = ANY($1) AND is_active`, affected); err != nil {
			return nil, internalError(err)
		}
		// Reports derived from the deleted source are no longer valid. Removing
		// runs cascades their insights and review records while preserving the
		// inactive workflow versions as history.
		if _, err := tx.Exec(ctx, `
			DELETE FROM runs
			WHERE workflow_id IN (SELECT id FROM workflows WHERE project_id = ANY($1))`, affected); err != nil {
			return nil, internalError(err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM data_sources WHERE id = $1`, id); err != nil {
		return nil, internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, internalError(err)
	}
	for _, projectID := range affected {
		s.publishProjectUpdated(projectID.String())
	}
	return &v1.DeleteSourceResponse{Deleted: true, AffectedProjectIds: uuidStrings(affected)}, nil
}

func (s *ConnectService) DeleteProject(ctx context.Context, req *v1.DeleteProjectRequest) (*v1.DeleteProjectResponse, error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "database unavailable")
	}
	rawID := strings.TrimSpace(req.GetId())
	id, err := uuid.Parse(rawID)
	if rawID == "" || err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "id must be a UUID")
	}
	tag, err := s.Store.Pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return nil, internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, connect.NewError(connect.CodeNotFound, "project not found")
	}
	s.publishProjectUpdated(id.String())
	return &v1.DeleteProjectResponse{Deleted: true}, nil
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func (s *ConnectService) publishProjectUpdated(projectID string) {
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: events.ProjectUpdated, ID: projectID})
	}
}

func sourceConfig(kind v1.SourceKind, config *v1.SourceConfig) (string, json.RawMessage, error) {
	if config == nil {
		return "", nil, errors.New("config is required")
	}
	switch kind {
	case v1.SourceKind_SOURCE_KIND_RSS:
		raw, err := json.Marshal(ingest.RSSConfig{URL: config.GetUrl()})
		if err != nil {
			return "", nil, err
		}
		if _, err := ingest.ParseRSSConfig(raw); err != nil {
			return "", nil, err
		}
		return "rss", raw, nil
	case v1.SourceKind_SOURCE_KIND_WEB:
		var schema json.RawMessage
		if config.GetSchema() != nil {
			encoded, err := protojson.Marshal(config.GetSchema())
			if err != nil {
				return "", nil, fmt.Errorf("encode web schema: %w", err)
			}
			schema = encoded
		}
		webConfig := ingest.WebConfig{
			URL: config.GetUrl(), Query: config.GetQuery(), Schema: schema,
			IDField: config.GetIdField(), RecordsPath: config.GetRecordsPath(),
			TimeField: config.GetTimeField(), Provider: config.GetProvider(),
		}
		if pagination := config.GetPagination(); pagination != nil {
			strategy := ""
			switch pagination.GetStrategy() {
			case v1.PaginationStrategy_PAGINATION_STRATEGY_UNSPECIFIED:
			case v1.PaginationStrategy_PAGINATION_STRATEGY_NEXT:
				strategy = "next"
			case v1.PaginationStrategy_PAGINATION_STRATEGY_PAGE_LINKS:
				strategy = "page_links"
			case v1.PaginationStrategy_PAGINATION_STRATEGY_SCROLL:
				strategy = "scroll"
			default:
				return "", nil, errors.New("pagination strategy is invalid")
			}
			webConfig.Pagination = &ingest.Pagination{
				Strategy: strategy, MaxPages: int(pagination.GetMaxPages()),
				MaxScrolls: int(pagination.GetMaxScrolls()), ScrollDelay: pagination.GetScrollDelaySeconds(),
				Selector: pagination.GetSelector(), NewestFirst: pagination.NewestFirst,
			}
		}
		raw, err := json.Marshal(webConfig)
		if err != nil {
			return "", nil, err
		}
		if _, err := ingest.ParseWebConfig(raw); err != nil {
			return "", nil, err
		}
		return "web", raw, nil
	case v1.SourceKind_SOURCE_KIND_CSV:
		return "", nil, errors.New("CSV sources must be created with UploadCSVSource")
	default:
		return "", nil, errors.New("kind must be RSS or WEB")
	}
}

func sourceMessage(source ingest.Source) (*v1.Source, error) {
	message := &v1.Source{
		Id: source.ID, Name: source.Name, Kind: sourceKind(source.Kind),
		RecordCount: int64(source.RecordCount), LastError: stringValue(source.LastError),
		CreatedAt: timestamppb.New(source.CreatedAt),
	}
	if source.LastPolledAt != nil {
		message.LastPolledAt = timestamppb.New(*source.LastPolledAt)
	}
	if source.PollIntervalS != nil {
		interval := int32(*source.PollIntervalS)
		message.PollIntervalSeconds = &interval
	}
	if len(source.Profile) > 0 && string(source.Profile) != "null" {
		var profile contracts.SourceProfile
		if err := json.Unmarshal(source.Profile, &profile); err != nil {
			return nil, fmt.Errorf("decode source profile: %w", err)
		}
		message.Profile = sourceProfileMessage(profile)
	}
	return message, nil
}

func sourceProfileMessage(profile contracts.SourceProfile) *v1.SourceProfile {
	message := &v1.SourceProfile{Summary: profile.Summary, Fields: make([]*v1.FieldProfile, 0, len(profile.Fields))}
	if profile.TimestampPath != nil {
		message.TimestampPath = profile.TimestampPath
	}
	for _, field := range profile.Fields {
		converted := &v1.FieldProfile{Path: field.Path, Role: fieldRole(field.Role), Description: field.Description}
		converted.Unit = field.Unit
		if field.Aggregation != nil {
			converted.Aggregation = aggregation(*field.Aggregation)
		}
		message.Fields = append(message.Fields, converted)
	}
	return message
}

func sourceKind(kind string) v1.SourceKind {
	switch kind {
	case "csv":
		return v1.SourceKind_SOURCE_KIND_CSV
	case "rss":
		return v1.SourceKind_SOURCE_KIND_RSS
	case "web":
		return v1.SourceKind_SOURCE_KIND_WEB
	default:
		return v1.SourceKind_SOURCE_KIND_UNSPECIFIED
	}
}

func fieldRole(role string) v1.FieldRole {
	switch role {
	case "timestamp":
		return v1.FieldRole_FIELD_ROLE_TIMESTAMP
	case "measure":
		return v1.FieldRole_FIELD_ROLE_MEASURE
	case "category":
		return v1.FieldRole_FIELD_ROLE_CATEGORY
	case "text":
		return v1.FieldRole_FIELD_ROLE_TEXT
	case "id":
		return v1.FieldRole_FIELD_ROLE_ID
	case "ignore":
		return v1.FieldRole_FIELD_ROLE_IGNORE
	default:
		return v1.FieldRole_FIELD_ROLE_UNSPECIFIED
	}
}

func aggregation(value string) v1.Aggregation {
	switch value {
	case "mean":
		return v1.Aggregation_AGGREGATION_MEAN
	case "sum":
		return v1.Aggregation_AGGREGATION_SUM
	case "last":
		return v1.Aggregation_AGGREGATION_LAST
	case "count":
		return v1.Aggregation_AGGREGATION_COUNT
	default:
		return v1.Aggregation_AGGREGATION_UNSPECIFIED
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sourceLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, "source not found")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return connect.NewError(connect.CodeInvalidArgument, "source id must be a UUID")
	}
	return internalError(err)
}

func internalError(err error) error {
	slog.Error("Connect RPC failed", "err", err)
	return connect.NewError(connect.CodeInternal, "internal server error")
}
