package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }
func fixture() (Request, Response) {
	req := Request{State: json.RawMessage(`"sample"`), Questions: map[string]Question{
		"bool": Noul("Is it relevant?"), "pick": Choice("Which?", map[string]string{"a": "A", "b": "B"}), "rank": Score("How relevant?", []string{"none", "partial", "full"}),
	}}
	resp := Response{Model: "jev-1.13.0", Usage: Usage{InputTokens: ptr(int64(12)), OutputTokens: ptr(int64(3))}, Answers: map[string]Answer{
		"bool": {Type: "noul", Noul: ptr(0.0)},
		"pick": {Type: "choice", Choice: "a", Confidence: ptr(0.9), Probabilities: map[string]*float64{"a": ptr(0.95), "b": ptr(0.05)}},
		"rank": {Type: "score", Score: ptr(1.5), Confidence: ptr(0.7), Probabilities: map[string]*float64{"0": ptr(0.0), "1": ptr(0.5), "2": ptr(0.5)}},
	}}
	return req, resp
}

func TestClientWire(t *testing.T) {
	for _, provider := range []string{"typesafe", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			req, want := fixture()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong HTTP contract")
				}
				var got Request
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				model := "jev-1.13.0"
				if provider == "openrouter" {
					model = "typesafe/jev-1.13"
				}
				if got.Model != model || len(got.Questions) != 3 {
					t.Errorf("bad request: %+v", got)
				}
				_ = json.NewEncoder(w).Encode(want)
			}))
			defer srv.Close()
			c, err := NewClient(provider, "secret", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			expectedEndpoint := "https://api.typesafe.ai/v1/systemone"
			if provider == "openrouter" {
				expectedEndpoint = "https://openrouter.ai/api/alpha/decisions"
			}
			if c.endpoint != expectedEndpoint {
				t.Fatal(c.endpoint)
			}
			expectedHost := "api.typesafe.ai"
			if provider == "openrouter" {
				expectedHost = "openrouter.ai"
			}
			if host := c.DestinationHost(); host != expectedHost {
				t.Fatalf("destination host = %q, want %q", host, expectedHost)
			}
			c.endpoint = srv.URL
			got, err := c.Evaluate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if *got.Answers["bool"].Noul != 0 || got.Usage.Cost != nil {
				t.Fatal("zero probability / unknown cost not preserved")
			}
		})
	}
}

func TestRejectMalformedAnswers(t *testing.T) {
	tests := map[string]func(*Response){
		"missing":              func(r *Response) { delete(r.Answers, "bool") },
		"extra":                func(r *Response) { r.Answers["unexpected"] = Answer{} },
		"null noul":            func(r *Response) { a := r.Answers["bool"]; a.Noul = nil; r.Answers["bool"] = a },
		"wrong type":           func(r *Response) { a := r.Answers["bool"]; a.Type = "choice"; r.Answers["bool"] = a },
		"outside enum":         func(r *Response) { a := r.Answers["pick"]; a.Choice = "injected"; r.Answers["pick"] = a },
		"missing probability":  func(r *Response) { delete(r.Answers["pick"].Probabilities, "b") },
		"null probability":     func(r *Response) { r.Answers["pick"].Probabilities["b"] = nil },
		"negative probability": func(r *Response) { r.Answers["pick"].Probabilities["b"] = ptr(-0.1) },
		"non unit sum":         func(r *Response) { r.Answers["pick"].Probabilities["b"] = ptr(0.9) },
		"not argmax":           func(r *Response) { a := r.Answers["pick"]; a.Choice = "b"; r.Answers["pick"] = a },
		"missing confidence":   func(r *Response) { a := r.Answers["pick"]; a.Confidence = nil; r.Answers["pick"] = a },
		"nonfinite confidence": func(r *Response) { a := r.Answers["pick"]; a.Confidence = ptr(math.NaN()); r.Answers["pick"] = a },
		"score mismatch":       func(r *Response) { a := r.Answers["rank"]; a.Score = ptr(0.3); r.Answers["rank"] = a },
		"missing usage":        func(r *Response) { r.Usage.InputTokens = nil },
		"negative cost":        func(r *Response) { r.Usage.Cost = ptr(-1.0) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req, resp := fixture()
			mutate(&resp)
			if resp.Validate(req) == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
}

func TestHTTPFailuresBoundedAndRedacted(t *testing.T) {
	for _, status := range []int{401, 403, 422, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "secret private-document")
			}))
			defer srv.Close()
			c, _ := NewClient("typesafe", "secret", time.Second)
			c.endpoint = srv.URL
			req, _ := fixture()
			_, err := c.Evaluate(context.Background(), req)
			var he *HTTPError
			if !errors.As(err, &he) || he.Status != status || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("bad failure: %v, calls %d", err, calls.Load())
			}
		})
	}
}

func TestRedirectAndCancellation(t *testing.T) {
	var leaked atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 307) }))
	defer src.Close()
	c, _ := NewClient("typesafe", "secret", time.Second)
	c.endpoint = src.URL
	req, _ := fixture()
	if _, err := c.Evaluate(context.Background(), req); err == nil || leaked.Load() {
		t.Fatal("followed redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Evaluate(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestBoundsAndInvalidJSON(t *testing.T) {
	for _, body := range []string{`{}`, `{"answers":`, strings.Repeat(" ", maxResponseBytes+1)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		c, _ := NewClient("typesafe", "secret", time.Second)
		c.endpoint = srv.URL
		req, _ := fixture()
		if _, err := c.Evaluate(context.Background(), req); err == nil {
			t.Fatal("accepted invalid response")
		}
		srv.Close()
	}
	c, _ := NewClient("typesafe", "secret", time.Second)
	req, _ := fixture()
	req.State, _ = json.Marshal(strings.Repeat("a", MaxRequestBytes))
	if _, err := c.Evaluate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatal(err)
	}
}

func TestRejectInvalidRequests(t *testing.T) {
	for _, req := range []Request{
		{State: json.RawMessage(`null`), Questions: map[string]Question{"q": Noul("?")}},
		{State: json.RawMessage(`"x"`)},
		{State: json.RawMessage(`"x"`), Questions: map[string]Question{"q": Choice("?", map[string]string{"a": "a"})}},
		{State: json.RawMessage(`"x"`), Questions: map[string]Question{"q": Score("?", []string{"a"})}},
		{State: json.RawMessage(`"x"`), Questions: map[string]Question{"q": Noul("")}},
	} {
		if req.Validate() == nil {
			t.Fatal("accepted invalid request")
		}
	}
}
