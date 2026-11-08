package ingest

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxCSVBytes = 20 * 1024 * 1024

type CSVConfig struct {
	URL       string `json:"url"`
	TimeField string `json:"time_field,omitempty"`
}

func ParseCSVConfig(raw json.RawMessage) (CSVConfig, error) {
	var cfg CSVConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid csv config: %w", err)
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return cfg, errors.New("csv config.url must be an absolute http(s) URL")
	}
	return cfg, nil
}

type CSVAdapter struct {
	Client *http.Client
	// PageDelay is the pause between pages during a backfill.
	PageDelay time.Duration
}

func (a *CSVAdapter) Kind() string { return "csv" }

// Fetch downloads the whole file and returns rows newest-first (rows without a
// timestamp are reversed, assuming the file is appended to) so the loop can
// cap backfills to the newest rows and add only new rows on later polls.
// During a backfill it also follows pages the server declares (HTTP Link
// rel="next", or a page/offset parameter already in the URL). CSV URLs are
// never probed with guessed parameters.
func (a *CSVAdapter) Fetch(ctx context.Context, raw json.RawMessage, opts FetchOptions) (FetchResult, error) {
	cfg, err := ParseCSVConfig(raw)
	if err != nil {
		return FetchResult{}, err
	}
	if !opts.Backfill || opts.Limit <= 0 {
		body, _, err := a.fetchPage(ctx, cfg.URL)
		if err != nil {
			return FetchResult{}, err
		}
		records, err := ParseCSV(body, cfg.TimeField, time.Now().UTC())
		if err == nil && !recordsHaveTimes(records) {
			reverseRecords(records)
		}
		return FetchResult{Records: records}, err
	}
	pager := &csvPager{adapter: a, cfg: cfg, now: time.Now().UTC()}
	records, report, err := CollectPages(ctx, pager, cfg.URL, PagingLimits{
		Limit:    opts.Limit,
		MaxPages: 50,
		Delay:    a.PageDelay,
	})
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Records: records, Report: &report}, nil
}

func (a *CSVAdapter) fetchPage(ctx context.Context, pageURL string) ([]byte, http.Header, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := doWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/csv, application/csv, text/plain")
		req.Header.Set("User-Agent", "AnduinDataPipeline/1.0")
		return req, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch csv: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, nil, fmt.Errorf("csv HTTP %d: %w", resp.StatusCode, ErrPageGone)
		}
		return nil, nil, fmt.Errorf("csv HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCSVBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read csv: %w", err)
	}
	if len(body) > MaxCSVBytes {
		return nil, nil, fmt.Errorf("csv exceeds %d bytes", MaxCSVBytes)
	}
	return body, resp.Header, nil
}

// maxCSVBackfillBytes bounds how much a paged CSV backfill downloads in
// total; each page is still capped at MaxCSVBytes.
const maxCSVBackfillBytes = 3 * MaxCSVBytes

type csvPager struct {
	adapter *CSVAdapter
	cfg     CSVConfig
	now     time.Time
	bytes   int // downloaded so far
}

func (p *csvPager) FetchPage(ctx context.Context, pageURL string, _ int) (Page, error) {
	body, header, err := p.adapter.fetchPage(ctx, pageURL)
	if err != nil {
		return Page{}, err
	}
	p.bytes += len(body)
	records, err := ParseCSV(body, p.cfg.TimeField, p.now)
	if err != nil {
		return Page{}, err
	}
	if !recordsHaveTimes(records) {
		// Without timestamps, assume the file is appended to: the bottom
		// rows are the newest.
		reverseRecords(records)
	}
	var candidates candidateList
	if p.bytes < maxCSVBackfillBytes {
		candidates.add(linkHeaderNext(header, pageURL), MethodLinkHeader, false)
		candidates.urlCandidates(pageURL, records, false, nil, false)
	}
	return Page{URL: pageURL, Records: records, Candidates: candidates}, nil
}

func ParseCSV(body []byte, timeField string, now time.Time) ([]RawRecord, error) {
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	reader := csv.NewReader(bytes.NewReader(body))
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}
	if len(headers) == 0 {
		return nil, errors.New("csv must have a header row")
	}
	for index := range headers {
		headers[index] = strings.TrimSpace(headers[index])
		if headers[index] == "" {
			return nil, fmt.Errorf("csv column %d has an empty name", index+1)
		}
	}
	if timeField == "" {
		timeField = detectCSVTimeField(headers)
	}

	var records []RawRecord
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv row: %w", err)
		}
		payload := make(map[string]any, len(headers))
		for index, header := range headers {
			payload[header] = csvValue(row[index])
		}
		observedAt, hasTime := now, false
		if timeField != "" {
			if value, ok := payload[timeField]; ok {
				if parsed, ok := parseCSVTime(value); ok {
					observedAt, hasTime = parsed, true
				}
			}
		}
		if !hasTime {
			payload["_time_estimated"] = true
		}
		externalID := hashRecord(payload)
		records = append(records, RawRecord{ExternalID: externalID, ObservedAt: observedAt, Payload: payload})
	}
	if len(records) == 0 {
		return nil, errors.New("csv must contain at least one data row")
	}
	return records, nil
}

func detectCSVTimeField(headers []string) string {
	for _, candidate := range []string{"date", "datetime", "time", "timestamp", "year", "month"} {
		for _, header := range headers {
			if strings.EqualFold(header, candidate) {
				return header
			}
		}
	}
	return ""
}

func parseCSVTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case int:
		if typed >= 1000 && typed <= 9999 {
			return time.Date(typed, 1, 1, 0, 0, 0, 0, time.UTC), true
		}
	case int64:
		if typed >= 1000 && typed <= 9999 {
			return time.Date(int(typed), 1, 1, 0, 0, 0, 0, time.UTC), true
		}
	case float64:
		if typed == float64(int(typed)) && typed >= 1000 && typed <= 9999 {
			return time.Date(int(typed), 1, 1, 0, 0, 0, 0, time.UTC), true
		}
	case string:
		if typed != "" {
			return parseTimestamp(typed)
		}
	}
	return time.Time{}, false
}

func csvValue(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.EqualFold(value, "true") {
		return true
	}
	if strings.EqualFold(value, "false") {
		return false
	}
	if integer, err := strconv.ParseInt(value, 10, 64); err == nil {
		return integer
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return number
	}
	return value
}
