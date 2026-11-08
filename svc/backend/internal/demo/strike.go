// Package demo seeds a realistic demo dataset into Postgres so the statistical
// author can be exercised end to end before ingestion and the profiler exist.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/contracts"
)

const (
	ProjectName = "Debug demo: strikes vs bus trips"
	Goal        = "Do bus strike announcements in the local news lead to fewer bus trips, and if so how many days later? Also flag anything unusual in recent trips."

	// PlantedLagDays is the true effect baked into the data: a strike
	// headline cuts trips PlantedLagDays days later by about 28%.
	PlantedLagDays = 2

	days = 180
	seed = 42
)

type Seeded struct {
	ProjectID uuid.UUID      `json:"project_id"`
	DataUpTo  time.Time      `json:"data_up_to"`
	Sources   []SeededSource `json:"sources"`
	Planted   string         `json:"planted"`
}

type SeededSource struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Records int       `json:"records"`
}

type record struct {
	externalID string
	observedAt time.Time
	payload    map[string]any
}

type source struct {
	name, kind string
	config     map[string]any
	profile    contracts.SourceProfile
	records    []record
}

func str(s string) *string { return &s }

// SeedStrikeDemo replaces any previous demo project with a fresh one: three
// sources (local news, daily bus trips, daily rainfall) over the 180 days
// ending the day before `now`. The data is deterministic apart from its dates.
//
// It is built to behave like real data: trips have a weekly cycle, noise and a
// few missing days; the news feed carries an unprofiled `outlet` field;
// rainfall is unrelated to anything; and the most recent trips day is an
// unrelated outlier. The only real relationship is strike news -> trips,
// PlantedLagDays later.
func SeedStrikeDemo(ctx context.Context, pool *pgxpool.Pool, now time.Time) (Seeded, error) {
	end := now.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	sources := build(end)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Seeded{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, `
		DELETE FROM data_sources WHERE id IN (
			SELECT ps.data_source_id FROM project_sources ps JOIN projects p ON p.id = ps.project_id WHERE p.name = $1)`,
		ProjectName); err != nil {
		return Seeded{}, fmt.Errorf("clear old sources: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM projects WHERE name = $1`, ProjectName); err != nil {
		return Seeded{}, fmt.Errorf("clear old project: %w", err)
	}

	out := Seeded{DataUpTo: lastObserved(sources), Planted: fmt.Sprintf(
		"strike headlines cut bus trips by ~28%% %d days later; rainfall is noise; the latest trips day is an unrelated outlier", PlantedLagDays)}
	if err := tx.QueryRow(ctx, `INSERT INTO projects (name, goal) VALUES ($1, $2) RETURNING id`, ProjectName, Goal).Scan(&out.ProjectID); err != nil {
		return Seeded{}, err
	}
	for i, s := range sources {
		cfg, _ := json.Marshal(s.config)
		profile, _ := json.Marshal(s.profile)
		var id uuid.UUID
		// created_at is staggered so source keys are stable (news, trips, rain).
		if err := tx.QueryRow(ctx, `
			INSERT INTO data_sources (name, kind, config, profile, created_at)
			VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5)) RETURNING id`,
			s.name, s.kind, cfg, profile, float64(i)).Scan(&id); err != nil {
			return Seeded{}, fmt.Errorf("insert source %s: %w", s.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_sources VALUES ($1, $2)`, out.ProjectID, id); err != nil {
			return Seeded{}, err
		}
		batch := &pgx.Batch{}
		for _, r := range s.records {
			p, _ := json.Marshal(r.payload)
			batch.Queue(`INSERT INTO data_records (data_source_id, external_id, observed_at, payload) VALUES ($1, $2, $3, $4)`,
				id, r.externalID, r.observedAt, p)
		}
		br := tx.SendBatch(ctx, batch)
		for range s.records {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return Seeded{}, fmt.Errorf("insert records for %s: %w", s.name, err)
			}
		}
		if err := br.Close(); err != nil {
			return Seeded{}, err
		}
		out.Sources = append(out.Sources, SeededSource{ID: id, Name: s.name, Kind: s.kind, Records: len(s.records)})
	}
	return out, tx.Commit(ctx)
}

func lastObserved(sources []source) time.Time {
	var last time.Time
	for _, s := range sources {
		for _, r := range s.records {
			if r.observedAt.After(last) {
				last = r.observedAt
			}
		}
	}
	return last
}

var (
	strikeHeadlines = []string{
		"Lothian Buses drivers announce strike action",
		"Bus strike: services to be cut across Edinburgh",
		"Union confirms walkout by bus staff",
	}
	otherHeadlines = []string{
		"Council approves new cycle lanes", "Festival organisers announce summer line-up", "Tram extension reaches next milestone",
		"Local bakery wins national award", "Roadworks planned for Princes Street", "Hearts and Hibs prepare for derby",
		"New library opens in Leith", "City centre parking charges to rise", "Storm warning issued for the east coast",
	}
	outlets = []string{"Edinburgh Echo", "Lothian Post", "Capital Times"}
)

func build(end time.Time) []source {
	rng := rand.New(rand.NewSource(seed))
	start := end.AddDate(0, 0, -(days - 1))
	at := func(day, hour, min int) time.Time {
		return start.AddDate(0, 0, day).Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute)
	}

	news := source{
		name: "Edinburgh news", kind: "rss", config: map[string]any{"url": "https://example.invalid/demo/news.xml"},
		profile: contracts.SourceProfile{
			Summary: "Local news headlines from Edinburgh outlets.",
			Fields:  []contracts.FieldProfile{{Path: "title", Role: "text", Description: "Headline text"}},
		},
	}
	trips := source{
		name: "Bus trips", kind: "csv", config: map[string]any{"file": "demo-bus-trips.csv"},
		profile: contracts.SourceProfile{
			Summary:       "Daily count of completed bus trips across the network.",
			TimestampPath: str("date"),
			Fields: []contracts.FieldProfile{
				{Path: "date", Role: "timestamp", Description: "Service day"},
				{Path: "trips", Role: "measure", Unit: str("trips"), Aggregation: str("sum"), Description: "Completed bus trips that day"},
			},
		},
	}
	rain := source{
		name: "Rainfall", kind: "csv", config: map[string]any{"file": "demo-rainfall.csv"},
		profile: contracts.SourceProfile{
			Summary:       "Daily rainfall in millimetres.",
			TimestampPath: str("date"),
			Fields: []contracts.FieldProfile{
				{Path: "date", Role: "timestamp", Description: "Day"},
				{Path: "rain_mm", Role: "measure", Unit: str("mm"), Aggregation: str("sum"), Description: "Rainfall that day"},
			},
		},
	}

	strike := make([]int, days) // strike headlines published on each day
	for d := 0; d < days; d++ {
		if rng.Float64() < 0.2 {
			strike[d] = 1 + rng.Intn(2)
		}
	}
	missing := map[int]bool{} // days the trips feed has no reading
	for len(missing) < 4 {
		missing[10+rng.Intn(days-30)] = true
	}

	for d := 0; d < days; d++ {
		date := start.AddDate(0, 0, d)
		ds := date.Format("2006-01-02")

		for k := 0; k < strike[d]; k++ {
			news.records = append(news.records, record{fmt.Sprintf("n-%s-s%d", ds, k), at(d, 7+rng.Intn(10), rng.Intn(60)),
				map[string]any{"title": strikeHeadlines[rng.Intn(len(strikeHeadlines))], "outlet": outlets[rng.Intn(len(outlets))]}})
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			news.records = append(news.records, record{fmt.Sprintf("n-%s-o%d", ds, k), at(d, 6+rng.Intn(14), rng.Intn(60)),
				map[string]any{"title": otherHeadlines[rng.Intn(len(otherHeadlines))], "outlet": outlets[rng.Intn(len(outlets))]}})
		}

		if !missing[d] {
			n := 52000.0
			if wd := date.Weekday(); wd == time.Saturday {
				n *= 0.82
			} else if wd == time.Sunday {
				n *= 0.7
			}
			if d >= PlantedLagDays && strike[d-PlantedLagDays] > 0 {
				n *= 0.72
			}
			n *= 1 + rng.NormFloat64()*0.03
			if d == days-1 {
				n *= 0.4 // unrelated outlier on the latest day
			}
			trips.records = append(trips.records, record{"t-" + ds, at(d, 23, 0), map[string]any{"date": ds, "trips": float64(int(n))}})
		}

		mm := 0.0
		if rng.Float64() < 0.45 {
			mm = float64(int(rng.ExpFloat64()*40)) / 10
		}
		rain.records = append(rain.records, record{"r-" + ds, at(d, 23, 30), map[string]any{"date": ds, "rain_mm": mm}})
	}
	return []source{news, trips, rain}
}
