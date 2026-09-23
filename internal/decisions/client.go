// Package decisions evaluates bounded semantic questions. It is deliberately
// separate from llm.Provider: Jev cannot generate prose or execute tool calls.
package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	MaxRequestBytes  = 96 * 1024 // Local pilot bound, NOT a tokenizer/context guarantee.
	maxResponseBytes = 1024 * 1024
	MaxQuestions     = 64
)

type Question struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type Request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Type          string              `json:"type"`
	Choice        string              `json:"choice,omitempty"`
	Noul          *float64            `json:"noul,omitempty"`
	Score         *float64            `json:"score,omitempty"`
	Confidence    *float64            `json:"confidence,omitempty"`
	Probabilities map[string]*float64 `json:"probabilities,omitempty"`
	Legend        map[string]string   `json:"legend,omitempty"`
}

type Usage struct {
	InputTokens  *int64   `json:"input_tokens"`
	OutputTokens *int64   `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"` // Provider-reported; nil is unknown, not free.
}

type Response struct {
	ID       string            `json:"id,omitempty"`
	Model    string            `json:"model"`
	Provider string            `json:"provider,omitempty"`
	Answers  map[string]Answer `json:"answers"`
	Usage    Usage             `json:"usage"`
}

type Evaluator interface {
	Evaluate(context.Context, Request) (Response, error)
}

// Client has a fixed destination and refuses redirects, including same-host
// redirects, to avoid replaying credentials or state at an unexpected endpoint.
// Calls are bounded and never retried implicitly (one call, one possible charge).
type Client struct {
	endpoint, key, model string
	http                 *http.Client
}

// DestinationHost reports the fixed endpoint host used by Evaluate. Routine
// egress policy derives its target from the client instead of maintaining a
// second provider-to-host table that could drift from the actual endpoint.
func (c *Client) DestinationHost() string {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func NewClient(provider, key string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("decision API key is missing")
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("decision timeout must be between 0 and 1 minute")
	}
	c := &Client{key: strings.TrimSpace(key), http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	switch provider {
	case "typesafe":
		c.endpoint = "https://api.typesafe.ai/v1/systemone"
		c.model = "jev-1.13.0"
	case "openrouter":
		c.endpoint = "https://openrouter.ai/api/alpha/decisions"
		c.model = "typesafe/jev-1.13"
	default:
		return nil, errors.New("decision provider must be typesafe or openrouter")
	}
	return c, nil
}

// HTTPError excludes upstream bodies: they can echo the supplied document/key.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("decision API returned HTTP %d", e.Status) }

func (c *Client) Evaluate(ctx context.Context, req Request) (Response, error) {
	var result Response
	if req.Model == "" {
		req.Model = c.model
	}
	if err := req.Validate(); err != nil {
		return result, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return result, errors.New("encode decision request")
	}
	if len(body) > MaxRequestBytes {
		return result, errors.New("decision request exceeds local byte limit")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("construct decision request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Do not propagate arbitrary transport errors, which may include headers.
		return result, errors.New("decision API transport failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, &HTTPError{Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return result, errors.New("read decision response")
	}
	if len(data) > maxResponseBytes {
		return result, errors.New("decision response exceeds byte limit")
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return Response{}, errors.New("invalid decision JSON response")
	}
	if err = result.Validate(req); err != nil {
		return Response{}, err
	}
	return result, nil
}

func Noul(instructions string) Question { return Question{Type: "noul", Instructions: instructions} }
func Choice(instructions string, options map[string]string) Question {
	b, _ := json.Marshal(options)
	return Question{Type: "choice", Instructions: instructions, Criteria: b}
}
func Score(instructions string, levels []string) Question {
	b, _ := json.Marshal(levels)
	return Question{Type: "score", Instructions: instructions, Criteria: b}
}

func (r Request) Validate() error {
	state := bytes.TrimSpace(r.State)
	if len(state) == 0 || !json.Valid(state) || (state[0] != '"' && state[0] != '{' && state[0] != '[') {
		return errors.New("decision state must be a JSON string, object, or array")
	}
	if len(r.Questions) == 0 || len(r.Questions) > MaxQuestions {
		return errors.New("decision request needs 1 to 64 questions")
	}
	for id, q := range r.Questions {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(q.Instructions) == "" {
			return errors.New("decision question needs an id and instructions")
		}
		switch q.Type {
		case "noul":
			if len(q.Criteria) > 0 {
				var criteria map[string]json.RawMessage
				if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) != 2 || criteria["true"] == nil || criteria["false"] == nil {
					return errors.New("noul criteria must describe true and false")
				}
			}
		case "choice":
			var options map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &options) != nil || len(options) < 2 || len(options) > 255 {
				return errors.New("choice needs 2 to 255 options")
			}
			for option := range options {
				if strings.TrimSpace(option) == "" {
					return errors.New("choice option cannot be empty")
				}
			}
		case "score":
			var levels []string
			if json.Unmarshal(q.Criteria, &levels) != nil || len(levels) < 2 || len(levels) > 10 {
				return errors.New("score needs 2 to 10 text levels")
			}
			for _, level := range levels {
				if strings.TrimSpace(level) == "" {
					return errors.New("score level cannot be empty")
				}
			}
		default:
			return errors.New("unknown decision question type")
		}
	}
	return nil
}

func unit(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1
}

// Validate checks the wire contract even when the provider promises type safety.
// Small rounding error (0.002) is tolerated but never silently renormalized.
func (r Response) Validate(req Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Model) == "" || len(r.Answers) != len(req.Questions) {
		return errors.New("incomplete decision response")
	}
	if r.Usage.InputTokens == nil || r.Usage.OutputTokens == nil || *r.Usage.InputTokens < 0 || *r.Usage.OutputTokens < 0 {
		return errors.New("missing or invalid decision usage")
	}
	if r.Usage.Cost != nil && (math.IsNaN(*r.Usage.Cost) || math.IsInf(*r.Usage.Cost, 0) || *r.Usage.Cost < 0) {
		return errors.New("invalid decision cost")
	}
	for id, q := range req.Questions {
		a, ok := r.Answers[id]
		if !ok || a.Type != q.Type {
			return errors.New("missing or mismatched decision answer")
		}
		if q.Type == "noul" {
			if !unit(a.Noul) || a.Score != nil || a.Choice != "" {
				return errors.New("invalid noul answer")
			}
			continue
		}
		if !unit(a.Confidence) || a.Noul != nil {
			return errors.New("invalid decision confidence")
		}
		keys := map[string]bool{}
		if q.Type == "choice" {
			var opts map[string]json.RawMessage
			_ = json.Unmarshal(q.Criteria, &opts)
			for k := range opts {
				keys[k] = true
			}
			if !keys[a.Choice] || a.Score != nil {
				return errors.New("choice outside allowed options")
			}
		} else {
			var levels []string
			_ = json.Unmarshal(q.Criteria, &levels)
			for i := range levels {
				keys[fmt.Sprint(i)] = true
			}
			if a.Score == nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) || *a.Score < 0 || *a.Score > float64(len(levels)-1) || a.Choice != "" {
				return errors.New("invalid score answer")
			}
		}
		if len(a.Probabilities) != len(keys) {
			return errors.New("incomplete decision probabilities")
		}
		sum, expected := 0.0, 0.0
		for k := range keys {
			p, ok := a.Probabilities[k]
			if !ok || !unit(p) {
				return errors.New("invalid decision probability")
			}
			sum += *p
			if q.Type == "choice" {
				chosen := a.Probabilities[a.Choice]
				if !unit(chosen) || *p > *chosen+0.000001 {
					return errors.New("choice is not the maximum probability")
				}
			} else {
				var i int
				_, _ = fmt.Sscan(k, &i)
				expected += float64(i) * (*p)
			}
		}
		if math.Abs(sum-1) > 0.002 {
			return errors.New("decision probabilities do not sum to one")
		}
		if q.Type == "score" && math.Abs(expected-*a.Score) > 0.02 {
			return errors.New("score disagrees with its probability distribution")
		}
	}
	return nil
}
