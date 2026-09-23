package fio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ErrStrongAuthRequired is Fio's 90-day SCA rule surfaced as a distinguishable
// error — callers can react to it specifically (e.g. seed the cursor via
// SetLastDate and retry) instead of treating it as an opaque Fio failure.
var ErrStrongAuthRequired = errors.New("fio: strong authorization (SCA) required for data this old")

// userAgent identifies every request this client makes to Fio.
const userAgent = "bank-system/1.0"

// FioClient calls Fio's classic export API. One FioClient is scoped to a single bank
// account's token — Fio's cursor state ("last downloaded transaction") lives
// server-side per token, not per request.
type FioClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	debug      bool
}

// NewClient builds a client against baseURL. No default/fallback here — the
// single source of truth for Fio's real URL is the required FIO_API_URL env
// var (see internal/config), not a value baked into this package; tests pass
// an httptest.NewServer URL instead.
func NewClient(baseURL, token string, debug bool) *FioClient {
	return &FioClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// The classic export API never legitimately redirects. Seeing one
			// usually means a WAF/bot-mitigation layer is intercepting the
			// request (e.g. rejecting it based on User-Agent) and sending back
			// its homepage instead of an error — which would otherwise surface
			// confusingly as a JSON decode failure once the client follows it.
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				return fmt.Errorf("unexpected redirect to %s", request.URL)
			},
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		debug:   debug,
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

func (c *FioClient) logRequest(request *http.Request) {
	if !c.debug {
		return
	}
	log.Printf("fio debug: request %s %s headers=%v", request.Method, c.redactToken(request.URL.String()), request.Header)
}

func (c *FioClient) logResponse(response *http.Response, body []byte) {
	if !c.debug {
		return
	}
	log.Printf("fio debug: response status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
}

// FetchNew returns transactions since the last successful FetchNew call for this
// token (Fio advances its server-side cursor as a side effect of a successful
// call). Prefer this over date-range queries for the daily sync — it can't
// re-fetch old data and doesn't require us to track our own "since" watermark.
func (c *FioClient) FetchNew(requestContext context.Context) (*TransactionsResponse, error) {
	url := fmt.Sprintf("%s/last/%s/transactions.json", c.baseURL, c.token)
	return c.get(requestContext, url)
}

// FetchPeriod returns transactions in [from, to], inclusive. Unlike FetchNew,
// this does not touch Fio's server-side cursor. Fio requires strong
// authorization (SCA, done in Fio's own Internet Banking UI) to serve a range
// older than 90 days — keep the range within that to avoid it.
func (c *FioClient) FetchPeriod(requestContext context.Context, from, to time.Time) (*TransactionsResponse, error) {
	url := fmt.Sprintf("%s/periods/%s/%s/%s/transactions.json", c.baseURL, c.token, from.Format("2006-01-02"), to.Format("2006-01-02"))
	return c.get(requestContext, url)
}

// RewindTo resets the server-side cursor to just after fioTransactionID, so the
// next FetchNew re-returns everything after it. Used as a best-effort recovery
// when a FetchNew succeeded (advancing Fio's cursor) but our own DB write then
// failed — without this, that batch would be permanently skipped since the
// unique constraint on raw_transactions only guards against re-inserting rows we
// already have, not rows we never received.
func (c *FioClient) RewindTo(requestContext context.Context, fioTransactionID int64) error {
	url := fmt.Sprintf("%s/set-last-id/%s/%d/", c.baseURL, c.token, fioTransactionID)
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	c.logRequest(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("fio set-last-id: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("reading fio response: %w", err)
	}
	c.logResponse(response, body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fio set-last-id: unexpected status %d", response.StatusCode)
	}
	return nil
}

// SetLastDate resets the server-side cursor to just after the given date, so
// the next FetchNew returns everything from that date forward. Unlike
// RewindTo (by transaction ID, used for failure recovery), this is by
// calendar date — used to seed a brand-new token's cursor to just inside the
// 90-day SCA window before the first-ever FetchNew, since Fio has no cursor
// established yet and would otherwise serve full history and hit
// ErrStrongAuthRequired.
func (c *FioClient) SetLastDate(requestContext context.Context, date time.Time) error {
	url := fmt.Sprintf("%s/set-last-date/%s/%s/", c.baseURL, c.token, date.Format("2006-01-02"))
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	c.logRequest(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("fio set-last-date: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("reading fio response: %w", err)
	}
	c.logResponse(response, body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fio set-last-date: unexpected status %d", response.StatusCode)
	}
	return nil
}

func (c *FioClient) get(requestContext context.Context, url string) (*TransactionsResponse, error) {
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	c.logRequest(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fio request: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("reading fio response: %w", err)
	}
	c.logResponse(response, body)

	// Fio returns 409 with an empty body when there's nothing new since the last
	// download — not an error, just zero transactions.
	if response.StatusCode == http.StatusConflict {
		return &TransactionsResponse{}, nil
	}
	if response.StatusCode == http.StatusUnprocessableEntity {
		return nil, fmt.Errorf("%w: %s", ErrStrongAuthRequired, body)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fio request: unexpected status %d: %s", response.StatusCode, body)
	}

	var out TransactionsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding fio response: %w", err)
	}
	return &out, nil
}
