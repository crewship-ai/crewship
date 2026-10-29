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
	craft.AddCommand(&cobra.Command{Use: "preview <page>", Args: cobra.ExactArgs(1), Short: "Read only the published native preview without opening an editor session", RunE: craftPreview})
	craft.AddCommand(&cobra.Command{Use: "connect <page>", Args: cobra.ExactArgs(1), Short: "Read bounded NDJSON actions; watch keeps the lease stream alive", RunE: craftConnect})
	initialize := &cobra.Command{Use: "initialize <page>", Args: cobra.ExactArgs(1), Short: "Initialize an existing empty owned Page as Word native; keep its admission in this process", RunE: craftInitialize}
	initialize.Flags().String("file", "", "Native Word document file (or - for stdin, without subsequent actions)")
	_ = initialize.MarkFlagRequired("file")
	craft.AddCommand(initialize)
	craft.AddCommand(newCraftAgentTaskCommand())
	return craft
}

type craftAction struct {
	Action     string                 `json:"action" yaml:"action"`
	Generation string                 `json:"generation,omitempty" yaml:"generation,omitempty"`
	RequestID  string                 `json:"request_id,omitempty" yaml:"request_id,omitempty"`
	ProposalID string                 `json:"proposal_id,omitempty" yaml:"proposal_id,omitempty"`
	Snapshot   *craftDocumentSnapshot `json:"snapshot,omitempty" yaml:"snapshot,omitempty"`
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
	return craftUseAdmission(cmd, args, c, r, false)
}

func craftUseAdmission(cmd *cobra.Command, args []string, c *cli.Client, r *http.Response, initialized bool) error {
	raw, e := craftReadJSON(r.Body)
	if e != nil {
		return e
	}
	var admission struct {
		Token       string `json:"session_token" yaml:"session_token"`
		ID          string `json:"session_id" yaml:"session_id"`
		Application string `json:"application_id" yaml:"application_id"`
		Page        string `json:"page_id" yaml:"page_id"`
		Generation  string `json:"generation" yaml:"generation"`
		Version     string `json:"version" yaml:"version"`
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
	// The scoped connection is a one-attempt transport. A same-origin307/308
	// must not replay a native mutation with its private admission header.
	httpClient := *c.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTPClient = &httpClient
	fields := map[string]string{"session_id": admission.ID}
	if initialized {
		if !craftPositiveVersion(admission.Generation) || admission.Version != "1" || admission.Application != "wordcraft" || admission.Page == "" {
			return fmt.Errorf("invalid native initialization response")
		}
		fields["application_id"] = admission.Application
		fields["page_id"] = admission.Page
		fields["generation"] = admission.Generation
		fields["version"] = admission.Version
	}
	public, _ := json.Marshal(fields)
	if e = craftEmit(cmd, public, token); e != nil {
		return e
	}
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 1024), craftNativeJSONLimit)
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
		if craftDocumentMutation(a.Action) {
			if e = craftValidateDocumentAction(a, admission.ID); e != nil {
				return e
			}
		} else if a.Snapshot != nil || len(scanner.Bytes()) > 16<<10 {
			return fmt.Errorf("invalid or oversized native action")
		}
		if a.Action == "proposal" || a.Action == "accept-proposal" || a.Action == "reject-proposal" {
			if !craftProposalID(a.ProposalID) {
				return fmt.Errorf("invalid proposal_id")
			}
		} else if a.ProposalID != "" {
			return fmt.Errorf("invalid native action")
		}
		if a.Action == "renew" || a.Action == "release" || a.Action == "handoff" || a.Action == "accept-proposal" {
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
		case "read":
			r, e = c.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/document")
		case "save":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/document/save", a.Snapshot)
		case "import":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/document/import", a.Snapshot)
		case "share":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/document/share", craftShareSelection{a.Snapshot.WorkspaceID, a.Snapshot.PageID, a.Snapshot.ApplicationID, a.Snapshot.EnginePin, a.Snapshot.SessionID, a.Snapshot.Generation, a.Snapshot.BaseVersion, a.Snapshot.TargetVersion})
		case "restore":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/document/restore", a.Snapshot)
		case "reconcile":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/document/reconcile", a.Snapshot)
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
		case "proposals":
			r, e = c.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/proposals")
		case "proposal":
			r, e = c.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/proposals/" + a.ProposalID)
		case "accept-proposal":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/proposals/"+a.ProposalID+"/accept", map[string]string{"sessionId": admission.ID, "generation": a.Generation})
		case "reject-proposal":
			r, e = c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/proposals/"+a.ProposalID+"/reject", struct{}{})
		case "watch":
			return craftWatch(cmd, c, args[0], token)
		default:
			return fmt.Errorf("unknown native action")
		}
		limit := int64(64 << 10)
		if a.Action == "read" || a.Action == "proposal" {
			limit = craftNativeJSONLimit
		}
		if e = craftResponseLimit(cmd, r, e, token, limit); e != nil {
			return e
		}
	}
	return scanner.Err()
}

// craftProposalID accepts only the server's opaque ncp_ identifier, so an
// action line cannot steer the request to another route.
func craftProposalID(id string) bool {
	rest, ok := strings.CutPrefix(id, "ncp_")
	if !ok || len(rest) != 24 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil && strings.ToLower(rest) == rest
}
func craftReadJSON(r io.Reader) (json.RawMessage, error) { return craftReadJSONLimit(r, 64<<10) }
func craftReadJSONLimit(r io.Reader, limit int64) (json.RawMessage, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil || int64(len(b)) > limit || !json.Valid(b) {
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
	formatter := resolvedFormatter(cmd)
	formatter.Writer = cmd.OutOrStdout()
	if cmd.Name() == "availability" {
		return formatter.Machine(clean(value))
	}
	return formatter.WriteStreamRow(clean(value))
}
func craftResponse(cmd *cobra.Command, r *http.Response, err error, secret string) error {
	return craftResponseLimit(cmd, r, err, secret, 64<<10)
}
func craftResponseLimit(cmd *cobra.Command, r *http.Response, err error, secret string, limit int64) error {
	if err != nil {
		return fmt.Errorf("native request failed")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return fmt.Errorf("native HTTP %d", r.StatusCode)
	}
	raw, e := craftReadJSONLimit(r.Body, limit)
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
						Delivery string `json:"delivery" yaml:"delivery"`
						Draft    string `json:"draft" yaml:"draft"`
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
