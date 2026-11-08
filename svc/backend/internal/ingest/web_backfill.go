package ingest

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Pseudo-URL prefixes handled by webPager instead of being crawled directly.
const (
	scrollPrefix  = "scroll:"  // scroll:<page url> → infinite-scroll extraction
	llmNextPrefix = "llmnext:" // llmnext:<page url> → ask the LLM for that page's next link
)

const llmNextQuestion = "Look at the pagination controls of this page. Return the absolute URL of the link " +
	"that loads the next (older) page of these results, or null if there is none."
const llmNextSchema = `{"type":"object","properties":{"next_url":{"type":["string","null"]}},"required":["next_url"]}`

// backfill walks pages until limit records are collected. Without an explicit
// config.pagination every generic method is tried (declared next links first,
// then guesses, then an LLM-located next link, then infinite scroll). An
// explicit pagination strategy restricts the walk to that strategy, with page
// and scroll caps raised to what the target needs.
func (a *WebAdapter) backfill(ctx context.Context, cfg WebConfig, limit int) (FetchResult, error) {
	pager := &webPager{adapter: a, cfg: cfg, auto: cfg.Pagination == nil}
	maxPages := clampInt(ceilDiv(limit, 20)+2, 1, 50)
	scroll := Pagination{MaxScrolls: clampInt(ceilDiv(limit, 10), 1, 100), ScrollDelay: 0.5}
	start := cfg.URL
	if p := cfg.Pagination; p != nil {
		pager.strategy = p.Strategy
		pager.selector = p.Selector
		if p.MaxPages > maxPages {
			maxPages = clampInt(p.MaxPages, 1, 50)
		}
		if p.MaxScrolls > scroll.MaxScrolls {
			scroll.MaxScrolls = clampInt(p.MaxScrolls, 1, 100)
		}
		if p.ScrollDelay > 0 {
			scroll.ScrollDelay = p.ScrollDelay
		}
		if p.Strategy == "scroll" {
			start = scrollPrefix + cfg.URL
			maxPages = 1
		}
	}
	pager.scroll = scroll
	records, report, err := CollectPages(ctx, pager, start, PagingLimits{
		Limit:     limit,
		MaxPages:  maxPages,
		MaxProbes: 3, // each probe costs an LLM extraction
	})
	if err != nil {
		return FetchResult{}, err
	}
	if start != cfg.URL {
		report.Methods[MethodScroll]++
	}
	return FetchResult{Records: records, Report: &report}, nil
}

// webPager fetches one web page per call: a single Crawl4AI crawl provides
// both the HTML for extraction and the links used to find the next page.
type webPager struct {
	adapter  *WebAdapter
	cfg      WebConfig
	auto     bool   // no explicit pagination: use every generic method
	strategy string // explicit strategy when !auto
	selector string
	scroll   Pagination

	// HTML of the last few pages fetched, used by the LLM next-link finder.
	// The finder only needs the page currently being walked from, but probes
	// may be fetched (and rejected) after it, so a few are kept.
	recent []cachedPage
}

type cachedPage struct{ url, html string }

const recentPagesKept = 4

func (p *webPager) remember(pageURL, pageHTML string) {
	p.recent = append(p.recent, cachedPage{url: pageURL, html: pageHTML})
	if len(p.recent) > recentPagesKept {
		p.recent = p.recent[len(p.recent)-recentPagesKept:]
	}
}

func (p *webPager) recall(pageURL string) string {
	for i := len(p.recent) - 1; i >= 0; i-- {
		if p.recent[i].url == pageURL {
			return p.recent[i].html
		}
	}
	return ""
}

func (p *webPager) FetchPage(ctx context.Context, pageURL string, want int) (Page, error) {
	if target, ok := strings.CutPrefix(pageURL, scrollPrefix); ok {
		cfg := p.cfg
		cfg.URL = target
		records, err := p.adapter.fetchScrolledWith(ctx, cfg, p.scroll, want, true)
		return Page{URL: pageURL, Records: records}, err
	}
	if source, ok := strings.CutPrefix(pageURL, llmNextPrefix); ok {
		next, err := p.findNextWithLLM(ctx, source)
		if err != nil {
			return Page{}, err
		}
		return p.fetchHTMLPage(ctx, next, want, false)
	}
	return p.fetchHTMLPage(ctx, pageURL, want, pageURL == p.cfg.URL)
}

func (p *webPager) fetchHTMLPage(ctx context.Context, pageURL string, want int, firstPage bool) (Page, error) {
	result, err := p.adapter.crawl(ctx, pageURL, map[string]any{"cache_mode": "bypass"})
	if err != nil {
		return Page{}, err
	}
	pageHTML := result.HTML
	if pageHTML == "" {
		pageHTML = result.CleanedHTML
	}
	source := pageURL
	if pageHTML != "" && len(pageHTML) <= maxCrawlHTMLBytes {
		source = "raw:" + pageHTML
	}
	extracted, err := p.adapter.llmJob(ctx, source, p.cfg, want)
	if err != nil {
		return Page{}, fmt.Errorf("extract %s: %w", pageURL, err)
	}
	records, err := recordsFromExtraction(extracted, p.cfg)
	if err != nil {
		return Page{}, fmt.Errorf("extract %s: %w", pageURL, err)
	}
	p.remember(pageURL, pageHTML)

	base := result.URL
	if base == "" {
		base = pageURL
	}
	links := crawlPageLinks(result, pageHTML, base, p.cfg.URL)
	return Page{URL: pageURL, Records: records, Candidates: p.candidates(pageURL, links, records, firstPage)}, nil
}

// candidates lists next-page options for an HTML page, best first.
func (p *webPager) candidates(pageURL string, links []pageLink, records []RawRecord, firstPage bool) candidateList {
	var c candidateList
	allow := func(strategy string) bool { return p.auto || p.strategy == strategy }

	if allow("next") {
		if p.selector != "" {
			// An explicit selector is a declaration, not a guess.
			c.add(anchorNext(links, p.selector), MethodAnchor, false)
		}
		c.add(relNext(links), MethodRelNext, false)
		if p.selector == "" {
			c.addKeyed(anchorNext(links, ""), MethodAnchor, true, "anchor")
		}
	}
	if allow("page_links") {
		c.add(nextNumberedLink(links, p.selector, pageURL), MethodPageNumber, false)
	}
	if p.auto {
		c.urlCandidates(pageURL, records, firstPage, []string{"page"}, false)
		// Last resorts: ask the LLM where the next page is, then scroll.
		c.addKeyed(llmNextPrefix+pageURL, MethodLLM, true, "llm")
		if firstPage {
			c.add(scrollPrefix+pageURL, MethodScroll, false)
		}
	}
	return c
}

// findNextWithLLM asks the extraction model for the next-page link of a page
// that was already crawled. It only returns same-host http(s) URLs.
func (p *webPager) findNextWithLLM(ctx context.Context, pageURL string) (string, error) {
	pageHTML := p.recall(pageURL)
	if pageHTML == "" || len(pageHTML) > maxCrawlHTMLBytes {
		return "", errors.New("no HTML to search for a next link")
	}
	answer, err := p.adapter.runLLM(ctx, "raw:"+pageHTML, llmNextQuestion+"\n\nReturn only JSON matching this JSON Schema:\n"+llmNextSchema, llmNextSchema, p.cfg.Provider)
	if err != nil {
		return "", err
	}
	href := findNextURL(answer)
	if href == "" {
		return "", errors.New("LLM found no next page link")
	}
	root, err := url.Parse(p.cfg.URL)
	if err != nil {
		return "", err
	}
	resolved, err := resolvePageURL(pageURL, href, root)
	if err != nil || resolved == "" || resolved == pageURL {
		return "", errors.New("LLM next page link is not usable")
	}
	return resolved, nil
}

func findNextURL(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if href, ok := typed["next_url"].(string); ok {
			return strings.TrimSpace(href)
		}
	case []any:
		for _, item := range typed {
			if href := findNextURL(item); href != "" {
				return href
			}
		}
	}
	return ""
}

var linkTagPattern = regexp.MustCompile(`(?is)<link\b([^>]*)>`)

// crawlPageLinks collects a page's links with their rel/class/aria attributes.
// The HTML is always parsed (Crawl4AI's own link list has no attributes), plus
// <link rel=…> tags from the head, then Crawl4AI's internal links are merged
// in for pages whose links are rendered by script.
func crawlPageLinks(result crawlResult, pageHTML, pageURL, rootURL string) []pageLink {
	root, err := url.Parse(rootURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var links []pageLink
	add := func(link pageLink) {
		if link.Href == "" {
			return
		}
		key := link.Href + "\x00" + link.Rel + "\x00" + link.Text
		if seen[key] {
			return
		}
		seen[key] = true
		links = append(links, link)
	}
	for _, match := range linkTagPattern.FindAllStringSubmatch(pageHTML, -1) {
		attributes := map[string]string{}
		for _, attr := range attrPattern.FindAllStringSubmatch(match[1], -1) {
			attributes[strings.ToLower(attr[1])] = html.UnescapeString(attr[2])
		}
		if !hasRelToken(attributes["rel"], "next") && !hasRelToken(attributes["rel"], "prev") {
			continue
		}
		if resolved, err := resolvePageURL(pageURL, attributes["href"], root); err == nil {
			add(pageLink{Href: resolved, Rel: attributes["rel"]})
		}
	}
	for _, link := range parseHTMLLinks(pageHTML, pageURL, rootURL) {
		add(link)
	}
	for _, link := range result.Links.Internal {
		if resolved, err := resolvePageURL(pageURL, link.Href, root); err == nil {
			add(pageLink{Href: resolved, Text: link.Text})
		}
	}
	return links
}

var trailingNumber = regexp.MustCompile(`(\d+)\D*$`)

// nextNumberedLink returns the numbered page link with the smallest page
// number above the current page's (page 1 when the URL has none).
func nextNumberedLink(links []pageLink, selector, pageURL string) string {
	current := currentPageNumber(pageURL)
	best, bestNumber := "", 0
	for _, href := range findPageLinks(links, selector) {
		n := linkPageNumber(href)
		if n > current && (best == "" || n < bestNumber) {
			best, bestNumber = href, n
		}
	}
	return best
}

func currentPageNumber(pageURL string) int {
	if n := linkPageNumber(pageURL); n > 0 {
		return n
	}
	return 1
}

func linkPageNumber(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	query := u.Query()
	for _, key := range pageQueryKeys {
		if n, err := strconv.Atoi(query.Get(key)); err == nil {
			return n
		}
	}
	if match := pathPagePattern.FindStringSubmatch(u.Path); match != nil {
		n, _ := strconv.Atoi(match[2])
		return n
	}
	if match := trailingNumber.FindStringSubmatch(u.Path); match != nil && pagePattern.MatchString(rawURL) {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	return 0
}
