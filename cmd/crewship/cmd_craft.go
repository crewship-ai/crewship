package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Native HUMAN sessions are process-local. Nothing stores or prints their bearer.
func init() { rootCmd.AddCommand(newCraftCommand()) }
func newCraftCommand() *cobra.Command {
	craft := &cobra.Command{Use: "craft", Short: "Use a native document's human edit session"}
	craft.AddCommand(&cobra.Command{Use: "availability <page>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := pageClient()
		if e != nil {
			return e
		}
		r, e := c.WithContext(cmd.Context()).Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/availability")
		return craftResponse(cmd, r, e, "")
	}})
	craft.AddCommand(&cobra.Command{Use: "connect <page>", Args: cobra.ExactArgs(1), Short: "Read bounded NDJSON actions; watch keeps the lease stream alive", RunE: craftConnect})
	return craft
}

type craftAction struct {
	Action     string `json:"action"`
	Generation string `json:"generation,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

func craftConnect(cmd *cobra.Command, args []string) error {
	c, e := pageClient()
	if e != nil {
		return e
	}
	c = c.WithContext(cmd.Context())
	r, e := c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/sessions", struct{}{})
	if e != nil {
		return fmt.Errorf("native session request failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 201 {
		return fmt.Errorf("native session HTTP %d", r.StatusCode)
	}
	raw, e := craftReadJSON(r.Body)
	if e != nil {
		return e
	}
	var admission struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if e = json.Unmarshal(raw, &admission); e != nil || len(admission.Token) != 64 || admission.ID == "" || admission.ID == admission.Token {
		return fmt.Errorf("invalid native session response")
	}
	decoded, err := hex.DecodeString(admission.Token)
	if err != nil || len(decoded) != 32 || strings.ToLower(admission.Token) != admission.Token {
		return fmt.Errorf("invalid native session response")
	}
	token := admission.Token
	c = c.WithHeader("X-Craft-Session", token)
	public, _ := json.Marshal(map[string]string{"session_id": admission.ID})
	if e = craftEmit(cmd, public, token); e != nil {
		return e
	}
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 1024), 16<<10)
	for n := 0; scanner.Scan(); n++ {
		if n >= 4096 {
			return fmt.Errorf("native action limit exceeded")
		}
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var a craftAction
		d := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		d.DisallowUnknownFields()
		if e = d.Decode(&a); e != nil {
			return fmt.Errorf("invalid native action")
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return fmt.Errorf("invalid native action")
		}
		if a.Action == "renew" || a.Action == "release" || a.Action == "handoff" {
			v, err := strconv.ParseInt(a.Generation, 10, 64)
			if err != nil || v < 1 || strconv.FormatInt(v, 10) != a.Generation {
				return fmt.Errorf("invalid native generation")
			}
		}
		body := map[string]string{}
		if a.Generation != "" {
			body["generation"] = a.Generation
		}
		if a.RequestID != "" {
			body["request_id"] = a.RequestID
		}
		switch a.Action {
		case "status":
			r, e = c.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/lease")
		case "acquire":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease", body)
		case "acquire-explicit":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease/acquire", body)
		case "renew":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease/renew", body)
		case "release":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease/release", body)
		case "request-handoff":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease/request-handoff", body)
		case "handoff":
			if a.RequestID == "" {
				return fmt.Errorf("handoff requires request_id")
			}
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/lease/handoff", body)
		case "reauthorize":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/sessions/reauthorize", struct{}{})
		case "watch":
			return craftWatch(cmd, c, args[0], token)
		default:
			return fmt.Errorf("unknown native action")
		}
		if e = craftResponse(cmd, r, e, token); e != nil {
			return e
		}
	}
	return scanner.Err()
}
func craftReadJSON(r io.Reader) (json.RawMessage, error) {
	b, e := io.ReadAll(io.LimitReader(r, (64<<10)+1))
	if e != nil || len(b) > 64<<10 || !json.Valid(b) {
		return nil, fmt.Errorf("invalid or oversized native JSON response")
	}
	return b, nil
}
func craftEmit(cmd *cobra.Command, raw json.RawMessage, secret string) error {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(&value); e != nil {
		return fmt.Errorf("invalid native JSON")
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				if k == "session_token" || secret != "" && strings.Contains(k, secret) {
					delete(x, k)
				} else {
					x[k] = clean(item)
				}
			}
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
		case string:
			if secret != "" {
				return strings.ReplaceAll(x, secret, "[redacted]")
			}
		}
		return v
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(clean(value))
}
func craftResponse(cmd *cobra.Command, r *http.Response, err error, secret string) error {
	if err != nil {
		return fmt.Errorf("native request failed")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return fmt.Errorf("native HTTP %d", r.StatusCode)
	}
	raw, e := craftReadJSON(r.Body)
	if e != nil {
		return e
	}
	return craftEmit(cmd, raw, secret)
}
func craftWatch(cmd *cobra.Command, c *cli.Client, page, secret string) error {
	// The shared WithTimeout(0) retains the ordinary timeout. Clone only this
	// stream HTTP client, preserving its original same-origin redirect policy.
	stream := *c
	hc := *c.HTTPClient
	hc.Timeout = 0
	stream.HTTPClient = &hc
	r, e := stream.Get("/api/v1/pages/" + pagePathEscape(page) + "/craft/events")
	if e != nil {
		return fmt.Errorf("native stream failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("native stream HTTP %d", r.StatusCode)
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("invalid native stream type")
	}
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 1024), 64<<10)
	var data strings.Builder
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				raw := json.RawMessage(strings.TrimSuffix(data.String(), "\n"))
				if !json.Valid(raw) {
					return fmt.Errorf("invalid native stream JSON")
				}
				if e = craftEmit(cmd, raw, secret); e != nil {
					return e
				}
				data.Reset()
				if event == "session_retired" {
					var retired struct {
						Delivery string `json:"delivery"`
						Draft    string `json:"draft"`
					}
					if json.Unmarshal(raw, &retired) != nil || retired.Delivery != "stopped" || (retired.Draft != "retain" && retired.Draft != "purge") {
						return fmt.Errorf("invalid native retirement event")
					}
					return nil
				}
			}
			event = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			part = strings.TrimPrefix(part, " ")
			if data.Len()+len(part) > 64<<10 {
				return fmt.Errorf("native stream event exceeds limit")
			}
			data.WriteString(part)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if ctx := cmd.Context(); ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("native stream disconnected: session state unconfirmed")
}
