package fio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const baseURL = "https://fioapi.fio.cz/v1/rest"

// FioClient calls Fio's classic export API. One FioClient is scoped to a single bank
// account's token — Fio's cursor state ("last downloaded transaction") lives
// server-side per token, not per request.
type FioClient struct {
	httpClient *http.Client
	token      string
	debug      bool
}

func NewClient(token string, debug bool) *FioClient {
	return &FioClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// The classic export API never legitimately redirects. Seeing one
			// usually means a WAF/bot-mitigation layer is intercepting the
			// request (e.g. rejecting it based on User-Agent) and sending back
			// its homepage instead of an error — which would otherwise surface
			// confusingly as a JSON decode failure once the client follows it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("unexpected redirect to %s", req.URL)
			},
		},
		token: token,
		debug: debug,
	}
}

// redactToken hides the token path segment so debug logs are safe to paste
// into issues/chat without leaking Fio API credentials.
func (c *FioClient) redactToken(url string) string {
	if c.token == "" {
		return url
	}
	return strings.ReplaceAll(url, c.token, "***REDACTED***")
}

func (c *FioClient) logRequest(req *http.Request) {
	if !c.debug {
		return
	}
	log.Printf("fio debug: request %s %s headers=%v", req.Method, c.redactToken(req.URL.String()), req.Header)
}

func (c *FioClient) logResponse(resp *http.Response, body []byte) {
	if !c.debug {
		return
	}
	log.Printf("fio debug: response status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
}

// FetchNew returns transactions since the last successful FetchNew call for this
// token (Fio advances its server-side cursor as a side effect of a successful
// call). Prefer this over date-range queries for the daily sync — it can't
// re-fetch old data and doesn't require us to track our own "since" watermark.
func (c *FioClient) FetchNew(ctx context.Context) (*TransactionsResponse, error) {
	url := fmt.Sprintf("%s/last/%s/transactions.json", baseURL, c.token)
	return c.get(ctx, url)
}

// FetchPeriod returns transactions in [from, to], inclusive. Unlike FetchNew,
// this does not touch Fio's server-side cursor. Fio requires strong
// authorization (SCA, done in Fio's own Internet Banking UI) to serve a range
// older than 90 days — keep the range within that to avoid it.
func (c *FioClient) FetchPeriod(ctx context.Context, from, to time.Time) (*TransactionsResponse, error) {
	url := fmt.Sprintf("%s/periods/%s/%s/%s/transactions.json", baseURL, c.token, from.Format("2006-01-02"), to.Format("2006-01-02"))
	return c.get(ctx, url)
}

// RewindTo resets the server-side cursor to just after fioTransactionID, so the
// next FetchNew re-returns everything after it. Used as a best-effort recovery
// when a FetchNew succeeded (advancing Fio's cursor) but our own DB write then
// failed — without this, that batch would be permanently skipped since the
// unique constraint on raw_transactions only guards against re-inserting rows we
// already have, not rows we never received.
func (c *FioClient) RewindTo(ctx context.Context, fioTransactionID int64) error {
	url := fmt.Sprintf("%s/set-last-id/%s/%d/", baseURL, c.token, fioTransactionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "bank-system/1.0")
	c.logRequest(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fio set-last-id: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading fio response: %w", err)
	}
	c.logResponse(resp, body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fio set-last-id: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *FioClient) get(ctx context.Context, url string) (*TransactionsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bank-system/1.0")
	c.logRequest(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fio request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading fio response: %w", err)
	}
	c.logResponse(resp, body)

	// Fio returns 409 with an empty body when there's nothing new since the last
	// download — not an error, just zero transactions.
	if resp.StatusCode == http.StatusConflict {
		return &TransactionsResponse{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fio request: unexpected status %d: %s", resp.StatusCode, body)
	}

	var out TransactionsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding fio response: %w", err)
	}
	return &out, nil
}
