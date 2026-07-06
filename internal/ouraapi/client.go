// Package ouraapi is a typed client for the Oura Ring API v2.
package ouraapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ouracli/oura/internal/envelope"
)

const (
	BaseURL        = "https://api.ouraring.com/v2"
	SandboxBaseURL = "https://api.ouraring.com/v2/sandbox"
	// sandboxToken satisfies the sandbox's "any non-empty token" requirement
	// so sandbox mode needs no credentials at all.
	sandboxToken = "sandbox"
)

// Client calls the Oura API. Zero value is not usable; use New.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a production client.
func New(token string, timeout time.Duration) *Client {
	return &Client{base: BaseURL, token: token, http: &http.Client{Timeout: timeout}}
}

// NewSandbox returns a client for the credential-free sandbox environment.
func NewSandbox(timeout time.Duration) *Client {
	return &Client{base: SandboxBaseURL, token: sandboxToken, http: &http.Client{Timeout: timeout}}
}

// ListResponse is the Oura collection envelope.
type ListResponse struct {
	Data      []json.RawMessage `json:"data"`
	NextToken *string           `json:"next_token"`
}

// apiError is Oura's error body: {"detail": "..."} (detail may also be a
// structured object on 422s, so keep it raw).
type apiError struct {
	Detail json.RawMessage `json:"detail"`
}

func (e apiError) message() string {
	var s string
	if json.Unmarshal(e.Detail, &s) == nil {
		return s
	}
	return string(e.Detail)
}

// Get performs a GET against path (e.g. "/usercollection/daily_sleep") with
// query params, decoding the response into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return envelope.New(envelope.KindInternal, "request_build", err.Error(), "")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return classifyTransportError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return envelope.New(envelope.KindNetwork, "read_body", err.Error(), "retry the request")
	}
	if resp.StatusCode != http.StatusOK {
		return classifyStatus(resp, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return envelope.New(envelope.KindAPI, "bad_response_json",
			fmt.Sprintf("Oura returned unparseable JSON: %v", err), "")
	}
	return nil
}

func classifyTransportError(err error) *envelope.Error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return envelope.New(envelope.KindNetwork, "timeout", err.Error(),
			"retry, or raise --timeout")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return envelope.New(envelope.KindNetwork, "dns", err.Error(),
			"check network connectivity to api.ouraring.com")
	}
	return envelope.New(envelope.KindNetwork, "connection", err.Error(),
		"retry with backoff; run 'oura doctor' if it persists")
}

func classifyStatus(resp *http.Response, body []byte) *envelope.Error {
	var ae apiError
	_ = json.Unmarshal(body, &ae)
	msg := ae.message()
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	msg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, msg)

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return envelope.New(envelope.KindAuth, "token_rejected", msg,
			"run 'oura auth login' to store a valid token, or check OURA_TOKEN")
	case http.StatusUpgradeRequired: // 426: Oura's "subscription required"
		return envelope.New(envelope.KindSubscription, "subscription_required", msg,
			"this data requires an active Oura membership")
	case http.StatusTooManyRequests:
		e := envelope.New(envelope.KindRateLimit, "rate_limited", msg,
			"wait before retrying; Oura allows 5000 requests per 5 minutes")
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				e.Hint = fmt.Sprintf("retry after %d seconds", secs)
			}
		}
		return e
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return envelope.New(envelope.KindUsage, "bad_request", msg,
			"check date formats (YYYY-MM-DD) and parameter names via 'oura schema'")
	default:
		if resp.StatusCode >= 500 {
			return envelope.New(envelope.KindAPI, "server_error", msg, "retry with backoff")
		}
		return envelope.New(envelope.KindAPI, "unexpected_status", msg, "")
	}
}
