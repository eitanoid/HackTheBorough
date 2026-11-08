package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxCrawlHTMLBytes = 2 * 1024 * 1024

type WebConfig struct {
	URL         string          `json:"url"`
	Query       string          `json:"query"`
	Schema      json.RawMessage `json:"schema"`
	IDField     string          `json:"id_field,omitempty"`
	RecordsPath string          `json:"records_path,omitempty"`
	TimeField   string          `json:"time_field,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	Pagination  *Pagination     `json:"pagination,omitempty"`
}

type Pagination struct {
	Strategy    string  `json:"strategy,omitempty"`
	MaxPages    int     `json:"max_pages,omitempty"`
	MaxScrolls  int     `json:"max_scrolls,omitempty"`
	ScrollDelay float64 `json:"scroll_delay,omitempty"`
	Selector    string  `json:"selector,omitempty"`
	NewestFirst *bool   `json:"newest_first,omitempty"`
}

func ParseWebConfig(raw json.RawMessage) (WebConfig, error) {
	var cfg WebConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid web config: %w", err)
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return cfg, errors.New("web config.url must be an absolute http(s) URL")
	}
	if len(cfg.Schema) == 0 {
		return cfg, errors.New("web config.schema is required")
	}
	schema, err := decodeJSON(cfg.Schema)
	if err != nil {
		return cfg, fmt.Errorf("web config.schema: %w", err)
	}
	if _, ok := schema.(map[string]any); !ok {
		return cfg, errors.New("web config.schema must be a JSON object")
	}
	if cfg.Query == "" {
		cfg.Query = "Extract all relevant records from this page."
	}
	if cfg.Pagination != nil {
		if cfg.Pagination.MaxPages == 0 {
			cfg.Pagination.MaxPages = 5
		}
		if cfg.Pagination.MaxPages < 1 || cfg.Pagination.MaxPages > 50 {
			return cfg, errors.New("pagination.max_pages must be from 1 to 50")
		}
		switch cfg.Pagination.Strategy {
		case "":
			cfg.Pagination.Strategy = "next"
		case "next", "page_links":
		case "scroll":
			if cfg.Pagination.MaxScrolls == 0 {
				cfg.Pagination.MaxScrolls = 10
			}
			if cfg.Pagination.MaxScrolls < 1 || cfg.Pagination.MaxScrolls > 100 {
				return cfg, errors.New("pagination.max_scrolls must be from 1 to 100")
			}
			if cfg.Pagination.ScrollDelay == 0 {
				cfg.Pagination.ScrollDelay = 0.5
			}
			if cfg.Pagination.ScrollDelay < 0 || cfg.Pagination.ScrollDelay > 5 {
				return cfg, errors.New("pagination.scroll_delay must be from 0 to 5")
			}
		default:
			return cfg, errors.New("pagination.strategy must be next, page_links, or scroll")
		}
	}
	return cfg, nil
}

type WebAdapter struct {
	Client      *http.Client
	Crawl4AIURL string
	Token       string
	Provider    string
}

func (a *WebAdapter) Kind() string { return "web" }

func (a *WebAdapter) Fetch(ctx context.Context, raw json.RawMessage, _ *time.Time) ([]RawRecord, error) {
	cfg, err := ParseWebConfig(raw)
	if err != nil {
		return nil, err
	}
	if cfg.Pagination != nil && cfg.Pagination.Strategy == "scroll" {
		return a.fetchScrolled(ctx, cfg)
	}

	pages, err := a.pageURLs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	newestFirst := true
	if cfg.Pagination != nil && cfg.Pagination.NewestFirst != nil {
		newestFirst = *cfg.Pagination.NewestFirst
	}

	seenRecords := map[string]bool{}
	var records []RawRecord
	for pageIndex, pageURL := range pages {
		extracted, err := a.extract(ctx, pageURL, cfg)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", pageURL, err)
		}
		if err := validateExtracted(extracted, cfg.Schema); err != nil {
			return nil, fmt.Errorf("extract %s: %w", pageURL, err)
		}
		items, err := recordsFromData(extracted, cfg.RecordsPath)
		if err != nil {
			return nil, err
		}
		newOnPage := 0
		for _, item := range items {
			record, err := rawRecordFromMap(item, cfg)
			if err != nil {
				return nil, err
			}
			if seenRecords[record.ExternalID] {
				continue
			}
			seenRecords[record.ExternalID] = true
			records = append(records, record)
			newOnPage++
		}
		if pageIndex > 0 && newestFirst && newOnPage == 0 {
			break
		}
	}
	return records, nil
}

func (a *WebAdapter) pageURLs(ctx context.Context, cfg WebConfig) ([]string, error) {
	if cfg.Pagination == nil {
		return []string{cfg.URL}, nil
	}
	switch cfg.Pagination.Strategy {
	case "next":
		pages := []string{cfg.URL}
		seen := map[string]bool{cfg.URL: true}
		for len(pages) < cfg.Pagination.MaxPages {
			links, err := a.fetchLinks(ctx, pages[len(pages)-1], cfg.URL)
			if err != nil {
				return nil, err
			}
			next := findNextPage(links, cfg.Pagination.Selector)
			if next == "" || seen[next] {
				break
			}
			seen[next] = true
			pages = append(pages, next)
		}
		return pages, nil
	case "page_links":
		links, err := a.fetchLinks(ctx, cfg.URL, cfg.URL)
		if err != nil {
			return nil, err
		}
		pages := []string{cfg.URL}
		seen := map[string]bool{cfg.URL: true}
		for _, link := range findPageLinks(links, cfg.Pagination.Selector) {
			if seen[link] {
				continue
			}
			if len(pages) == cfg.Pagination.MaxPages {
				break
			}
			seen[link] = true
			pages = append(pages, link)
		}
		return pages, nil
	}
	return []string{cfg.URL}, nil
}

func (a *WebAdapter) fetchScrolled(ctx context.Context, cfg WebConfig) ([]RawRecord, error) {
	result, err := a.crawl(ctx, cfg.URL, map[string]any{
		"scan_full_page":   true,
		"max_scroll_steps": cfg.Pagination.MaxScrolls,
		"scroll_delay":     cfg.Pagination.ScrollDelay,
		"cache_mode":       "bypass",
	})
	if err != nil {
		return nil, err
	}
	pageHTML := result.HTML
	if pageHTML == "" {
		pageHTML = result.CleanedHTML
	}
	if pageHTML == "" {
		return nil, errors.New("scroll returned no HTML")
	}
	if len(pageHTML) > maxCrawlHTMLBytes {
		return nil, fmt.Errorf("scrolled HTML exceeds %d bytes", maxCrawlHTMLBytes)
	}
	extracted, err := a.llmJob(ctx, "raw:"+pageHTML, cfg)
	if err != nil {
		return nil, err
	}
	if err := validateExtracted(extracted, cfg.Schema); err != nil {
		return nil, err
	}
	items, err := recordsFromData(extracted, cfg.RecordsPath)
	if err != nil {
		return nil, err
	}
	records := make([]RawRecord, 0, len(items))
	for _, item := range items {
		record, err := rawRecordFromMap(item, cfg)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (a *WebAdapter) extract(ctx context.Context, pageURL string, cfg WebConfig) (any, error) {
	return a.llmJob(ctx, pageURL, cfg)
}

type llmJobRequest struct {
	URL      string `json:"url"`
	Question string `json:"q"`
	Schema   string `json:"schema,omitempty"`
	Cache    bool   `json:"cache"`
	Provider string `json:"provider,omitempty"`
}

type llmJobResponse struct {
	TaskID string          `json:"task_id"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (a *WebAdapter) llmJob(ctx context.Context, pageURL string, cfg WebConfig) (any, error) {
	if strings.TrimSpace(a.Token) == "" {
		return nil, errors.New("CRAWL4AI_API_TOKEN is not set")
	}
	schema := strings.TrimSpace(string(cfg.Schema))
	question := cfg.Query + "\n\nReturn only JSON matching this JSON Schema:\n" + schema
	provider := cfg.Provider
	if provider == "" {
		provider = a.Provider
	}
	body, _ := json.Marshal(llmJobRequest{
		URL: pageURL, Question: question, Schema: schema, Cache: false, Provider: provider,
	})
	var response llmJobResponse
	if err := a.postJSON(ctx, "/llm/job", body, &response); err != nil {
		return nil, err
	}
	if response.TaskID == "" {
		if len(response.Result) > 0 {
			return normalizeExtraction(response.Result)
		}
		return nil, errors.New("Crawl4AI did not return an LLM task id")
	}

	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		var status llmJobResponse
		if err := a.getJSON(ctx, "/llm/job/"+url.PathEscape(response.TaskID), &status); err != nil {
			return nil, err
		}
		switch status.Status {
		case "completed":
			if len(status.Result) == 0 {
				return nil, errors.New("Crawl4AI LLM job completed without a result")
			}
			return normalizeExtraction(status.Result)
		case "failed":
			return nil, fmt.Errorf("Crawl4AI LLM extraction failed: %s", rawError(status.Error))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, errors.New("Crawl4AI LLM extraction timed out")
}

type crawlResult struct {
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message"`
	URL          string `json:"url"`
	HTML         string `json:"html"`
	CleanedHTML  string `json:"cleaned_html"`
	Links        struct {
		Internal []crawlLink `json:"internal"`
		External []crawlLink `json:"external"`
	} `json:"links"`
}

type crawlLink struct {
	Href  string `json:"href"`
	Text  string `json:"text"`
	Title string `json:"title"`
}

type crawlResponse struct {
	Success bool          `json:"success"`
	Results []crawlResult `json:"results"`
}

func (a *WebAdapter) fetchLinks(ctx context.Context, pageURL, rootURL string) ([]pageLink, error) {
	result, err := a.crawl(ctx, pageURL, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Links.Internal) > 0 {
		links := make([]pageLink, 0, len(result.Links.Internal))
		for _, link := range result.Links.Internal {
			links = append(links, pageLink{Href: link.Href, Text: link.Text})
		}
		return links, nil
	}
	return parseHTMLLinks(result.HTML, result.URL, rootURL), nil
}

func (a *WebAdapter) crawl(ctx context.Context, pageURL string, crawlerParams map[string]any) (crawlResult, error) {
	if strings.TrimSpace(a.Token) == "" {
		return crawlResult{}, errors.New("CRAWL4AI_API_TOKEN is not set")
	}
	request := map[string]any{"urls": []string{pageURL}}
	if crawlerParams != nil {
		request["crawler_config"] = map[string]any{"type": "CrawlerRunConfig", "params": crawlerParams}
	}
	body, _ := json.Marshal(request)
	var response crawlResponse
	if err := a.postJSON(ctx, "/crawl", body, &response); err != nil {
		return crawlResult{}, err
	}
	if !response.Success || len(response.Results) == 0 {
		return crawlResult{}, errors.New("Crawl4AI returned no page")
	}
	result := response.Results[0]
	if !result.Success && result.HTML == "" {
		if result.ErrorMessage == "" {
			result.ErrorMessage = "Crawl4AI page failed"
		}
		return crawlResult{}, errors.New(result.ErrorMessage)
	}
	return result, nil
}

func (a *WebAdapter) postJSON(ctx context.Context, path string, body []byte, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(a.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return a.doJSON(req, target)
}

func (a *WebAdapter) getJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(path), nil)
	if err != nil {
		return err
	}
	if token := strings.TrimSpace(a.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return a.doJSON(req, target)
}

func (a *WebAdapter) doJSON(req *http.Request, target any) error {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 210 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Crawl4AI request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Crawl4AI HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode Crawl4AI response: %w", err)
	}
	return nil
}

func (a *WebAdapter) endpoint(path string) string {
	base := strings.TrimRight(a.Crawl4AIURL, "/")
	base = strings.TrimSuffix(base, "/llm")
	return base + path
}

func validateExtracted(value any, rawSchema json.RawMessage) error {
	decoded, err := decodeJSON(rawSchema)
	if err != nil {
		return err
	}
	schema, ok := decoded.(map[string]any)
	if !ok {
		return errors.New("source schema must be a JSON object")
	}
	// Crawl4AI returns a list of records even when the schema describes a
	// single record, so validate each element instead of the array itself.
	if records, ok := value.([]any); ok && schema["type"] == "object" {
		for i, record := range records {
			if err := validateJSONSchema(record, schema); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	}
	return validateJSONSchema(value, schema)
}

func normalizeExtraction(raw json.RawMessage) (any, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode extracted answer: %w", err)
	}
	switch typed := value.(type) {
	case []any:
		cleaned := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized, err := normalizeExtractionItem(item)
			if err != nil {
				return nil, err
			}
			cleaned = append(cleaned, normalized)
		}
		if len(cleaned) == 0 {
			return nil, errors.New("Crawl4AI returned an empty answer")
		}
		return cleaned, nil
	default:
		return normalizeExtractionItem(value)
	}
}

func normalizeExtractionItem(value any) (any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	if failed, _ := record["error"].(bool); failed {
		message, _ := record["content"].(string)
		if message == "" {
			message = "Crawl4AI extraction item failed"
		}
		return nil, errors.New(message)
	}
	if content, ok := record["content"].(string); ok && looksLikeEnvelope(record) {
		parsed, err := decodeJSON([]byte(content))
		if err != nil {
			return nil, fmt.Errorf("extracted content is not JSON: %w", err)
		}
		return normalizeExtractionItem(parsed)
	}
	delete(record, "error")
	delete(record, "index")
	delete(record, "tags")
	return record, nil
}

func looksLikeEnvelope(record map[string]any) bool {
	for key := range record {
		if key != "content" && key != "error" && key != "index" && key != "tags" {
			return false
		}
	}
	return true
}

func rawError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "unknown error"
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return string(raw)
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func recordsFromData(data any, recordsPath string) ([]map[string]any, error) {
	if recordsPath != "" {
		current := data
		for _, part := range strings.Split(recordsPath, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("records_path %q was not found", recordsPath)
			}
			current, ok = object[part]
			if !ok {
				return nil, fmt.Errorf("records_path %q was not found", recordsPath)
			}
		}
		data = current
	}
	var records []map[string]any
	switch typed := data.(type) {
	case []any:
		for _, item := range typed {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("schema must return an object or an array of objects")
			}
			records = append(records, record)
		}
	case map[string]any:
		records = []map[string]any{typed}
	default:
		return nil, errors.New("schema must return an object or an array of objects")
	}
	return records, nil
}

var stableIDFields = []string{"id", "guid", "url", "link", "external_id", "post_id", "article_id"}

func rawRecordFromMap(record map[string]any, cfg WebConfig) (RawRecord, error) {
	externalID := ""
	if cfg.IDField != "" {
		value, ok := valueAtPath(record, cfg.IDField)
		if !ok || value == nil || isComposite(value) || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return RawRecord{}, fmt.Errorf("record is missing scalar id field %q", cfg.IDField)
		}
		externalID = fmt.Sprint(value)
	}
	if externalID == "" {
		for _, field := range stableIDFields {
			if value, ok := record[field]; ok && value != nil && !isComposite(value) && strings.TrimSpace(fmt.Sprint(value)) != "" {
				externalID = fmt.Sprint(value)
				break
			}
		}
	}
	if externalID == "" {
		externalID = hashRecord(record)
	}

	observedAt := time.Now().UTC()
	hasTimestamp := false
	timestampField := cfg.TimeField
	if timestampField == "" {
		for _, candidate := range []string{"published_at", "published", "date", "timestamp", "created_at", "updated_at"} {
			if _, ok := valueAtPath(record, candidate); ok {
				timestampField = candidate
				break
			}
		}
	}
	if timestampField != "" {
		if value, ok := valueAtPath(record, timestampField); ok {
			if parsed, ok := parseTimestamp(fmt.Sprint(value)); ok {
				observedAt = parsed
				hasTimestamp = true
				record["published_at"] = parsed.Format(time.RFC3339)
			}
		}
	}
	if !hasTimestamp {
		record["_time_estimated"] = true
	}
	return RawRecord{ExternalID: externalID, ObservedAt: observedAt, Payload: record}, nil
}

func valueAtPath(value any, path string) (any, bool) {
	current := value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func isComposite(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

type pageLink struct {
	Href  string
	Text  string
	Rel   string
	Class string
	Aria  string
}

var anchorPattern = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
var attrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
var tagPattern = regexp.MustCompile(`(?is)<[^>]+>`)
var pagePattern = regexp.MustCompile(`(?i)/(?:page|pages|p)/\d+(?:/|$)|[?&](?:page|pages|p)=\d+`)

func parseHTMLLinks(pageHTML, pageURL, rootURL string) []pageLink {
	root, err := url.Parse(rootURL)
	if err != nil {
		return nil
	}
	links := []pageLink{}
	for _, match := range anchorPattern.FindAllStringSubmatch(pageHTML, -1) {
		attributes := map[string]string{}
		for _, attr := range attrPattern.FindAllStringSubmatch(match[1], -1) {
			attributes[strings.ToLower(attr[1])] = html.UnescapeString(attr[2])
		}
		href := attributes["href"]
		if href == "" {
			continue
		}
		resolved, err := resolvePageURL(pageURL, href, root)
		if err != nil || resolved == "" {
			continue
		}
		text := strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(match[2], "")))
		links = append(links, pageLink{
			Href: resolved, Text: text, Rel: attributes["rel"],
			Class: attributes["class"], Aria: attributes["aria-label"],
		})
	}
	return links
}

func resolvePageURL(pageURL, href string, root *url.URL) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", err
	}
	target, err := base.Parse(href)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return "", errors.New("not an http(s) link")
	}
	if !strings.EqualFold(target.Hostname(), root.Hostname()) {
		return "", errors.New("different host")
	}
	target.Fragment = ""
	return target.String(), nil
}

func findNextPage(links []pageLink, selector string) string {
	for _, link := range links {
		if selector != "" {
			if matchesPageSelector(link, selector) {
				return link.Href
			}
			continue
		}
		rel := strings.ToLower(link.Rel)
		text := strings.ToLower(link.Text)
		aria := strings.ToLower(link.Aria)
		class := strings.ToLower(link.Class)
		if strings.Contains(rel, "next") || text == "next" || strings.Contains(text, "next") ||
			strings.Contains(aria, "next") || strings.Contains(class, "next") {
			return link.Href
		}
	}
	return ""
}

func findPageLinks(links []pageLink, selector string) []string {
	var pages []string
	seen := map[string]bool{}
	for _, link := range links {
		if selector != "" && !matchesPageSelector(link, selector) {
			continue
		}
		if selector == "" && !pagePattern.MatchString(link.Href) {
			continue
		}
		if !seen[link.Href] {
			seen[link.Href] = true
			pages = append(pages, link.Href)
		}
	}
	return pages
}

func lastSelectorSegment(selector string) string {
	depth := 0
	start := 0
	for index, char := range selector {
		switch char {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ' ', '>':
			if depth == 0 {
				start = index + 1
			}
		}
	}
	return strings.TrimSpace(selector[start:])
}

func matchesPageSelector(link pageLink, selector string) bool {
	last := lastSelectorSegment(selector)
	if last == "" || last == "*" {
		return true
	}
	if strings.HasPrefix(last, ".") {
		return containsToken(link.Class, strings.TrimPrefix(last, "."))
	}
	if strings.HasPrefix(last, "#") {
		return false
	}
	if tagEnd := strings.IndexAny(last, ".#["); tagEnd > 0 {
		last = last[tagEnd:]
	} else if !strings.ContainsAny(last, ".#[") {
		return last == "a"
	}
	if strings.HasPrefix(last, ".") {
		name := strings.TrimPrefix(last, ".")
		if end := strings.IndexAny(name, ".#["); end >= 0 {
			name = name[:end]
		}
		return containsToken(link.Class, name)
	}
	if strings.HasPrefix(last, "[") && strings.HasSuffix(last, "]") {
		expression := strings.TrimSuffix(strings.TrimPrefix(last, "["), "]")
		parts := strings.SplitN(expression, "=", 2)
		name := strings.TrimSpace(parts[0])
		expected := ""
		if len(parts) == 2 {
			expected = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
		actual := ""
		switch strings.ToLower(name) {
		case "rel":
			actual = link.Rel
		case "class":
			actual = link.Class
		case "aria-label":
			actual = link.Aria
		case "href":
			actual = link.Href
		}
		return expected == "" && actual != "" || actual == expected
	}
	return false
}

func containsToken(value, token string) bool {
	for _, part := range strings.Fields(value) {
		if part == token {
			return true
		}
	}
	return false
}
