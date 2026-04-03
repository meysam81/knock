// Package main provides a CLI tool for submitting sitemap URLs to the
// Google Indexing API for crawl/index requests.
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
)

const (
	indexingAPIEndpoint = "https://indexing.googleapis.com/v3/urlNotifications:publish"
	indexingAPIScope    = "https://www.googleapis.com/auth/indexing"
)

// URLSet represents a sitemap XML structure.
type URLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []SitemapURL `xml:"url"`
}

// SitemapURL represents a single URL entry in a sitemap.
type SitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// IndexingRequest is the payload for the Google Indexing API.
type IndexingRequest struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

// IndexingResponse captures the API response.
type IndexingResponse struct {
	URLNotificationMetadata struct {
		URL          string                       `json:"url"`
		LatestUpdate *struct{ NotifyTime string } `json:"latestUpdate,omitempty"`
		LatestRemove *struct{ NotifyTime string } `json:"latestRemove,omitempty"`
	} `json:"urlNotificationMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func main() {
	sitemapURL := flag.String("sitemap", "", "URL of the sitemap to process, e.g. https://example.com/sitemap.xml")
	credsFile := flag.String("credentials", "", "Path to GCP service account JSON key file (or set GOOGLE_APPLICATION_CREDENTIALS).")
	dryRun := flag.Bool("dry-run", false, "Parse sitemap and print URLs without submitting.")
	concurrency := flag.Int("concurrency", 2, "Number of concurrent API requests.")
	delayMs := flag.Int("delay-ms", 1000, "Delay between requests in milliseconds (per worker).")
	notifType := flag.String("type", "URL_UPDATED", "Notification type: URL_UPDATED or URL_DELETED.")
	flag.Parse()

	if *sitemapURL == "" {
		slog.Error("--sitemap flag is required.")
		os.Exit(1)
	}

	if *notifType != "URL_UPDATED" && *notifType != "URL_DELETED" {
		slog.Error("--type must be URL_UPDATED or URL_DELETED.")
		os.Exit(1)
	}

	urls, err := fetchSitemap(*sitemapURL)
	if err != nil {
		slog.Error("fetch sitemap", "error", err)
		os.Exit(1)
	}

	slog.Info("parsed sitemap", "urls", len(urls), "source", *sitemapURL)

	if *dryRun {
		for _, u := range urls {
			fmt.Println(u.Loc)
		}
		return
	}

	ctx := context.Background()

	client, err := newIndexingClient(ctx, *credsFile)
	if err != nil {
		slog.Error("create indexing client", "error", err)
		os.Exit(1)
	}

	results := submitAll(ctx, client, urls, *notifType, *concurrency, time.Duration(*delayMs)*time.Millisecond)

	var succeeded, failed int
	for _, r := range results {
		if r.err != nil {
			failed++
			slog.Error("submit failed", "url", r.url, "error", r.err)
		} else {
			succeeded++
			slog.Info("submit ok", "url", r.url)
		}
	}

	slog.Info("done", "total", len(urls), "succeeded", succeeded, "failed", failed)

	if failed > 0 {
		os.Exit(1)
	}
}

// fetchSitemap retrieves and parses a sitemap XML from the given URL.
func fetchSitemap(sitemapURL string) ([]SitemapURL, error) {
	resp, err := http.Get(sitemapURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", sitemapURL, err)
	}
	defer closeBody(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", sitemapURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var urlSet URLSet
	if err := xml.Unmarshal(body, &urlSet); err != nil {
		return nil, fmt.Errorf("parse sitemap XML: %w", err)
	}

	return urlSet.URLs, nil
}

// newIndexingClient creates an authenticated HTTP client for the Indexing API.
func newIndexingClient(ctx context.Context, credsFile string) (*http.Client, error) {
	if credsFile != "" {
		data, err := os.ReadFile(credsFile)
		if err != nil {
			return nil, fmt.Errorf("read credentials file %s: %w", credsFile, err)
		}

		conf, err := google.JWTConfigFromJSON(data, indexingAPIScope)
		if err != nil {
			return nil, fmt.Errorf("parse credentials: %w", err)
		}

		return conf.Client(ctx), nil
	}

	// Fall back to GOOGLE_APPLICATION_CREDENTIALS / workload identity.
	client, err := google.DefaultClient(ctx, indexingAPIScope)
	if err != nil {
		return nil, fmt.Errorf("default credentials: %w", err)
	}

	return client, nil
}

type submitResult struct {
	url string
	err error
}

// submitAll sends indexing requests for all URLs with bounded concurrency.
func submitAll(
	ctx context.Context,
	client *http.Client,
	urls []SitemapURL,
	notifType string,
	workers int,
	delay time.Duration,
) []submitResult {
	results := make([]submitResult, len(urls))
	ch := make(chan int, len(urls))

	for i := range urls {
		ch <- i
	}
	close(ch)

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			defer wg.Done()
			for idx := range ch {
				results[idx] = submitResult{
					url: urls[idx].Loc,
					err: submitURL(ctx, client, urls[idx].Loc, notifType),
				}
				time.Sleep(delay)
			}
		})
	}

	wg.Wait()

	return results
}

// submitURL sends a single URL notification to the Indexing API.
func submitURL(ctx context.Context, client *http.Client, rawURL, notifType string) error {
	payload, err := json.Marshal(IndexingRequest{
		URL:  rawURL,
		Type: notifType,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, indexingAPIEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST indexing API: %w", err)
	}
	defer closeBody(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var idxResp IndexingResponse
	if err := json.Unmarshal(body, &idxResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if idxResp.Error != nil {
		return fmt.Errorf("API error %d: %s", idxResp.Error.Code, idxResp.Error.Message)
	}

	return nil
}

func closeBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
