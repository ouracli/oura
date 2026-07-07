package ouraapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ouracli/oura/internal/envelope"
)

// newTestClient builds a Client pointed at srv with the given bearer token.
func newTestClient(srv *httptest.Server, token string) *Client {
	return &Client{base: srv.URL, token: token, http: srv.Client()}
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantKind   envelope.Kind
		wantHint   string // substring, empty = don't check
	}{
		{"400 bad request", http.StatusBadRequest, `{"detail":"bad date"}`, "", envelope.KindUsage, ""},
		{"401 unauthorized", http.StatusUnauthorized, `{"detail":"invalid token"}`, "", envelope.KindAuth, "auth login"},
		{"403 forbidden", http.StatusForbidden, `{"detail":"missing scope"}`, "", envelope.KindAuth, "auth login"},
		{"404 not found", http.StatusNotFound, `{"detail":"no such document"}`, "", envelope.KindUsage, ""},
		{"422 unprocessable", http.StatusUnprocessableEntity, `{"detail":[{"loc":["query","start_date"],"msg":"bad"}]}`, "", envelope.KindUsage, ""},
		{"426 upgrade required", http.StatusUpgradeRequired, `{"detail":"subscription required"}`, "", envelope.KindSubscription, ""},
		{"429 rate limited without Retry-After", http.StatusTooManyRequests, `{"detail":"slow down"}`, "", envelope.KindRateLimit, "5000 requests"},
		{"429 rate limited with Retry-After", http.StatusTooManyRequests, `{"detail":"slow down"}`, "42", envelope.KindRateLimit, "42 seconds"},
		{"500 server error", http.StatusInternalServerError, `{"detail":"boom"}`, "", envelope.KindAPI, "retry"},
		{"unexpected 5xx", http.StatusBadGateway, `{"detail":"boom"}`, "", envelope.KindAPI, ""},
		{"unexpected other status", http.StatusTeapot, `{"detail":"teapot"}`, "", envelope.KindAPI, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			if tc.retryAfter != "" {
				resp.Header.Set("Retry-After", tc.retryAfter)
			}
			err := classifyStatus(resp, []byte(tc.body))
			if err.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", err.Kind, tc.wantKind)
			}
			if err.Code != envelope.ExitCode(tc.wantKind) {
				t.Errorf("Code = %d, want %d", err.Code, envelope.ExitCode(tc.wantKind))
			}
			if tc.wantHint != "" && !strings.Contains(err.Hint, tc.wantHint) {
				t.Errorf("Hint = %q, want it to contain %q", err.Hint, tc.wantHint)
			}
		})
	}
}

func TestGetSuccessDecodesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"1"}],"next_token":null}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "test-token")
	var out ListResponse
	if err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("got %d docs, want 1", len(out.Data))
	}
	if out.NextToken != nil {
		t.Errorf("NextToken = %v, want nil", out.NextToken)
	}
}

func TestGetErrorStatusClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"bad token"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "bad-token")
	err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, nil)
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	envErr, ok := err.(*envelope.Error)
	if !ok {
		t.Fatalf("error is %T, want *envelope.Error", err)
	}
	if envErr.Kind != envelope.KindAuth {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindAuth)
	}
}

func TestGetBadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	var out ListResponse
	err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, &out)
	if err == nil {
		t.Fatal("expected an error decoding non-JSON body")
	}
	envErr := err.(*envelope.Error)
	if envErr.Kind != envelope.KindAPI {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindAPI)
	}
}

func TestGetBodyCapEnforced(t *testing.T) {
	// The client caps response bodies at 16MB (io.LimitReader). Serve a body
	// just over that cap and confirm the client does not hang or OOM trying
	// to read it all: json.Unmarshal on the truncated (and therefore corrupt)
	// body must fail cleanly as a KindAPI error, never a KindInternal panic.
	const cap = 16 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[`))
		chunk := make([]byte, 4096)
		for i := range chunk {
			chunk[i] = 'a'
		}
		written := 9
		for written < cap+1024 {
			n, err := w.Write(chunk)
			written += n
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	var out ListResponse
	err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, &out)
	if err == nil {
		t.Fatal("expected an error since the truncated body is not valid JSON")
	}
	envErr, ok := err.(*envelope.Error)
	if !ok {
		t.Fatalf("error is %T, want *envelope.Error", err)
	}
	if envErr.Kind != envelope.KindAPI {
		t.Errorf("Kind = %q, want %q (LimitReader truncation should surface as a bad-response error, not a crash)", envErr.Kind, envelope.KindAPI)
	}
}

func TestListAllFollowsPagination(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("next_token") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"next_token":"page2"}`))
		case "page2":
			_, _ = w.Write([]byte(`{"data":[{"id":"3"}],"next_token":null}`))
		default:
			t.Errorf("unexpected next_token %q", r.URL.Query().Get("next_token"))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	ep := Endpoint{CLI: "sleep", Path: "/usercollection/daily_sleep", Style: StyleDateRange}

	var ids []string
	err := c.ListAll(context.Background(), ep, url.Values{"start_date": {"2026-06-29"}}, func(doc json.RawMessage) error {
		var m map[string]string
		if err := json.Unmarshal(doc, &m); err != nil {
			return err
		}
		ids = append(ids, m["id"])
		return nil
	})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("made %d requests, want 2 (one per page)", len(requests))
	}
	want := []string{"1", "2", "3"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestListAllStopsOnEmitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"next_token":"page2"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	ep := Endpoint{CLI: "sleep", Path: "/usercollection/daily_sleep", Style: StyleDateRange}

	stopErr := fmt.Errorf("stop here")
	calls := 0
	err := c.ListAll(context.Background(), ep, url.Values{}, func(doc json.RawMessage) error {
		calls++
		return stopErr
	})
	if err != stopErr {
		t.Errorf("ListAll error = %v, want %v", err, stopErr)
	}
	if calls != 1 {
		t.Errorf("emit called %d times, want 1 (should stop on first error)", calls)
	}
}

func TestListRejectsSingleObjectStyle(t *testing.T) {
	c := &Client{base: "http://unused.invalid", token: "t", http: http.DefaultClient}
	ep := Endpoint{CLI: "profile", Style: StyleSingleObject}
	_, err := c.List(context.Background(), ep, nil)
	if err == nil {
		t.Fatal("List on a StyleSingleObject endpoint should error")
	}
	if envErr := err.(*envelope.Error); envErr.Kind != envelope.KindUsage {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindUsage)
	}
}

func TestObjectRejectsNonSingleObjectStyle(t *testing.T) {
	c := &Client{base: "http://unused.invalid", token: "t", http: http.DefaultClient}
	ep := Endpoint{CLI: "sleep", Style: StyleDateRange}
	_, err := c.Object(context.Background(), ep)
	if err == nil {
		t.Fatal("Object on a non-StyleSingleObject endpoint should error")
	}
	if envErr := err.(*envelope.Error); envErr.Kind != envelope.KindUsage {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindUsage)
	}
}

func TestDocRejectsEndpointWithoutDocRoute(t *testing.T) {
	c := &Client{base: "http://unused.invalid", token: "t", http: http.DefaultClient}
	ep := Endpoint{CLI: "heartrate", Style: StyleDatetimeRange, HasDocID: false}
	_, err := c.Doc(context.Background(), ep, "some-id", "")
	if err == nil {
		t.Fatal("Doc on an endpoint without HasDocID should error")
	}
	if envErr := err.(*envelope.Error); envErr.Kind != envelope.KindUsage {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindUsage)
	}
}

func TestDocPassesFieldsProjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usercollection/daily_sleep/doc-1" {
			t.Errorf("path = %s, want /usercollection/daily_sleep/doc-1", r.URL.Path)
		}
		if got := r.URL.Query().Get("fields"); got != "score,day" {
			t.Errorf("fields query = %q, want %q", got, "score,day")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"doc-1"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	ep := Endpoint{CLI: "sleep", Path: "/usercollection/daily_sleep", Style: StyleDateRange, HasDocID: true, HasFields: true,
		Fields: []string{"day", "id", "score"}}
	raw, err := c.Doc(context.Background(), ep, "doc-1", "score,day")
	if err != nil {
		t.Fatalf("Doc: %v", err)
	}
	if string(raw) != `{"id":"doc-1"}` {
		t.Errorf("raw = %s", raw)
	}
}

func TestObjectFetchesBareDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usercollection/personal_info" {
			t.Errorf("path = %s, want /usercollection/personal_info", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"me","email":"a@b.com"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "t")
	ep := Endpoint{CLI: "profile", Path: "/usercollection/personal_info", Style: StyleSingleObject}
	raw, err := c.Object(context.Background(), ep)
	if err != nil {
		t.Fatalf("Object: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["email"] != "a@b.com" {
		t.Errorf("email = %q, want %q", m["email"], "a@b.com")
	}
}

// TestSandboxClientSendsDummyToken confirms NewSandbox() satisfies the Oura
// sandbox's "any non-empty Authorization header" requirement without any
// credentials being configured, and that it targets the sandbox base URL.
func TestSandboxClientSendsDummyToken(t *testing.T) {
	if sandboxToken == "" {
		t.Fatal("sandboxToken must be non-empty")
	}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"next_token":null}`))
	}))
	defer srv.Close()

	c := NewSandbox(5 * time.Second)
	c.base = srv.URL // redirect from the real sandbox host to our test server
	if err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, &ListResponse{}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth == "" || gotAuth == "Bearer " {
		t.Errorf("Authorization header = %q, want a non-empty bearer token", gotAuth)
	}
}

func TestNewSandboxTargetsSandboxBaseURL(t *testing.T) {
	c := NewSandbox(5 * time.Second)
	if c.base != SandboxBaseURL {
		t.Errorf("base = %q, want %q", c.base, SandboxBaseURL)
	}
}

func TestNewTargetsProductionBaseURL(t *testing.T) {
	c := New("tok", 5*time.Second)
	if c.base != BaseURL {
		t.Errorf("base = %q, want %q", c.base, BaseURL)
	}
	if c.token != "tok" {
		t.Errorf("token = %q, want %q", c.token, "tok")
	}
}

// TestClassifyTransportErrorTimeout exercises the network-timeout branch of
// classifyTransportError via a client with an aggressively short timeout
// against a server that stalls.
func TestClassifyTransportErrorTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{base: srv.URL, token: "t", http: &http.Client{Timeout: time.Millisecond}}
	err := c.Get(context.Background(), "/usercollection/daily_sleep", nil, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	envErr, ok := err.(*envelope.Error)
	if !ok {
		t.Fatalf("error is %T, want *envelope.Error", err)
	}
	if envErr.Kind != envelope.KindNetwork {
		t.Errorf("Kind = %q, want %q", envErr.Kind, envelope.KindNetwork)
	}
	if envErr.Reason != "timeout" {
		t.Errorf("Reason = %q, want %q", envErr.Reason, "timeout")
	}
}
