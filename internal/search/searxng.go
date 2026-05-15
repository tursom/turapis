package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type searchResponse struct {
	Query           string         `json:"query"`
	NumberOfResults int            `json:"number_of_results"`
	Results         []searchResult `json:"results"`
	Answers         []string       `json:"answers,omitempty"`
	Suggestions     []string       `json:"suggestions,omitempty"`
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Engine  string `json:"engine"`
}

type Client struct {
	baseURL string
	client  *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

type SearchInput struct {
	Query string
	Limit int
}

type SearchOutput struct {
	Query   string
	Results []Result
	Answers []string
}

type Result struct {
	Title   string
	URL     string
	Snippet string
}

func (c *Client) Search(ctx context.Context, input SearchInput) (*SearchOutput, error) {
	u, err := url.Parse(c.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("parse searxng url: %w", err)
	}
	q := url.Values{}
	q.Set("q", input.Query)
	q.Set("format", "json")
	q.Set("categories", "general")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng returned HTTP %d", resp.StatusCode)
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode searxng response: %w", err)
	}

	limit := input.Limit
	if limit <= 0 || limit > len(sr.Results) {
		limit = len(sr.Results)
	}
	if limit > 5 {
		limit = 5
	}

	out := &SearchOutput{
		Query:   sr.Query,
		Answers: sr.Answers,
	}
	for i := range limit {
		r := sr.Results[i]
		out.Results = append(out.Results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	return out, nil
}

func (s *SearchOutput) FormatAsContext() string {
	var b strings.Builder
	b.WriteString("\n\n[Web Search Results]\n")
	if s.Query != "" {
		b.WriteString("Query: " + s.Query + "\n\n")
	}
	for i, r := range s.Results {
		b.WriteString(fmt.Sprintf("%d. %s\n   URL: %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet))
	}
	if len(s.Answers) > 0 {
		b.WriteString("Direct answers:\n")
		for _, a := range s.Answers {
			b.WriteString("- " + a + "\n")
		}
	}
	return b.String()
}
