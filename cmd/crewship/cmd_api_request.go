package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const apiInputLimit = 10 << 20

// Only origin-relative, unambiguous paths are accepted. Do not use ResolveReference:
// an absolute URL, //host, or traversal must never retarget the authenticated call.
func apiRequestPath(raw string, query []string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", apiValidation("PATH must be an origin-relative path such as /api/v1/agents, without a fragment")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", apiValidation("PATH must not contain traversal segments")
		}
	}
	if strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.ContainsAny(u.Path, "{}") {
		return "", apiValidation("PATH contains invalid characters or unresolved path parameters")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", apiValidation("invalid query string")
	}
	for _, pair := range query {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return "", apiValidation("--query requires name=value")
		}
		q.Add(key, value)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func readAPIInput(cmd *cobra.Command, input, contentType string, limit int64) ([]byte, error) {
	if input == "" {
		return nil, nil
	}
	var reader io.Reader = cmd.InOrStdin()
	if input != "-" {
		f, err := os.Open(input)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, apiValidation("request body exceeds --max-input-bytes")
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, apiValidation("invalid --content-type")
	}
	if (media == "application/json" || strings.HasSuffix(media, "+json")) && !json.Valid(data) {
		return nil, apiValidation("--input must contain exactly one valid JSON value")
	}
	return data, nil
}

func newAPIRequestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "request METHOD PATH", Short: "Call an HTTP API endpoint using the active server, token, and workspace", Args: apiArgs(cobra.ExactArgs(2)), Example: `  crewship api request GET /api/v1/agents --query limit=20 --format json
  crewship api request POST /api/v1/agents --input agent.json --dry-run
  crewship api request POST /api/v1/agents --input agent.json --yes
  crewship api request GET /openapi.json --output server-openapi.json`, Long: `Send one HTTP request. PATH is relative to the configured server, never a URL.
POST, PUT, PATCH, and DELETE require --yes; no interactive prompt is opened.
--dry-run validates inputs and prints a plan without network access or reading
stdin/files. It omits query values and body contents. It does not validate the
body against OpenAPI or predict server authorization.

JSON is validated before sending. Use --input - for explicit stdin, or a file
to avoid putting secrets in shell history. For non-JSON input set --content-type.
Responses default to JSON; non-JSON downloads require --output. Files are written
atomically with owner-only permissions. Requests are never automatically retried
or redirected. For large downloads increase --max-response-bytes explicitly.
Server authorization, approval gates, and scope restrictions remain in force.`}
	cmd.Flags().StringArray("query", nil, "Query parameter name=value (repeatable; values are URL-encoded)")
	cmd.Flags().String("input", "", "Request body file, or - for stdin")
	cmd.Flags().Int64("max-input-bytes", apiInputLimit, "Maximum request body bytes (must be positive)")
	cmd.Flags().StringArray("header", nil, "Additional header name=value (repeatable; authentication and transport headers are managed by the CLI)")
	cmd.Flags().String("content-type", "application/json", "Request body media type")
	cmd.Flags().String("output", "", "Save raw response atomically to a file instead of stdout")
	cmd.Flags().Bool("include", false, "Wrap JSON output with HTTP status and response headers (e.g. ETag)")
	cmd.Flags().String("idempotency-key", "", "Idempotency-Key for endpoints that support it; no automatic retries")
	cmd.Flags().Bool("yes", false, "Explicitly allow a mutating HTTP method")
	cmd.Flags().Bool("dry-run", false, "Show request metadata without network access or reading input")
	cmd.Flags().Duration("timeout", 30*time.Second, "Overall request timeout (must be positive)")
	cmd.Flags().Int64("max-response-bytes", 10<<20, "Maximum response bytes (must be positive)")
	cmd.RunE = runAPIRequest
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeAPIMethod(cmd, args, prefix)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

func runAPIRequest(cmd *cobra.Command, args []string) error {
	method := strings.ToUpper(args[0])
	if !apiHTTPMethod(method) {
		return apiValidation("unsupported HTTP method; use GET, HEAD, OPTIONS, POST, PUT, PATCH, or DELETE")
	}
	query, _ := cmd.Flags().GetStringArray("query")
	path, err := apiRequestPath(args[1], query)
	if err != nil {
		return err
	}
	input, _ := cmd.Flags().GetString("input")
	output, _ := cmd.Flags().GetString("output")
	include, _ := cmd.Flags().GetBool("include")
	if include && output != "" {
		return apiValidation("--include and --output cannot be combined")
	}
	contentType, _ := cmd.Flags().GetString("content-type")
	key, _ := cmd.Flags().GetString("idempotency-key")
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	limit, _ := cmd.Flags().GetInt64("max-response-bytes")
	inputLimit, _ := cmd.Flags().GetInt64("max-input-bytes")
	headerArgs, _ := cmd.Flags().GetStringArray("header")
	headers, err := apiRequestHeaders(headerArgs)
	if err != nil {
		return err
	}
	if timeout <= 0 || limit <= 0 || limit == int64(^uint64(0)>>1) || inputLimit <= 0 || inputLimit == int64(^uint64(0)>>1) {
		return apiValidation("--timeout, --max-input-bytes, and --max-response-bytes must be positive and within range")
	}
	if (method == "GET" || method == "HEAD") && input != "" {
		return apiValidation("GET and HEAD must not have --input")
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return apiValidation("invalid --content-type")
	}
	if strings.ContainsAny(key, "\r\n\x00") {
		return apiValidation("invalid --idempotency-key")
	}
	f := newFormatter()
	switch f.Format {
	case "", "table", "json", "yaml", "ndjson", "quiet":
	default:
		return apiValidation("unsupported output format")
	}
	client := newAPIClient().WithContext(cmd.Context()).WithTimeout(timeout)
	server, err := url.Parse(client.BaseURL)
	if err != nil || (server.Scheme != "http" && server.Scheme != "https") || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return apiValidation("server must be an HTTP(S) URL without credentials, query, or fragment")
	}
	client.BaseURL = strings.TrimRight(client.BaseURL, "/")
	if dryRun {
		u, _ := url.Parse(path)
		keys := make([]string, 0, len(u.Query()))
		for k := range u.Query() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		headerNames := make([]string, 0, len(headers))
		for name := range headers {
			headerNames = append(headerNames, name)
		}
		sort.Strings(headerNames)
		return apiStructuredOutput(cmd, map[string]any{"method": method, "server": server.String(), "path": u.EscapedPath(), "query_keys": keys, "header_names": headerNames, "workspace": client.WorkspaceID, "has_body": input != "", "content_type": contentType, "requires_yes": apiRequiresYes(method), "dry_run": true})
	}
	if apiRequiresYes(method) && !yes {
		return apiValidation("mutating requests require --yes; inspect with --dry-run first")
	}
	if err := requireAuth(); err != nil {
		return err
	}
	data, err := readAPIInput(cmd, input, contentType, inputLimit)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != "" {
		body = bytes.NewReader(data)
	}
	// Raw requests must not follow even same-origin redirects: 301/302/303 can
	// silently turn a requested mutation into GET, and 307/308 can replay a body.
	hc := *client.HTTPClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.HTTPClient = &hc
	req, err := client.NewRequest(cmd.Context(), method, path, body)
	if err != nil {
		return err
	}
	for name, values := range headers {
		req.Header[name] = values
	}
	if input != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if output == "" && req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	// Prepare an output file before a potentially mutating request, so an invalid
	// destination cannot turn a successful server-side mutation into a local error.
	var dest *cli.AtomicFile
	if output != "" {
		dest, err = cli.NewAtomicFile(output)
		if err != nil {
			return err
		}
		defer dest.Close()
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return &cli.ConnectionError{Err: err}
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if dest != nil {
		n, err := io.Copy(dest, io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return err
		}
		if n > limit {
			return fmt.Errorf("response exceeds --max-response-bytes; output file was not replaced")
		}
		return dest.Commit()
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("response exceeds --max-response-bytes")
	}
	var value any
	if len(bytes.TrimSpace(data)) != 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return fmt.Errorf("response is not JSON; use --output to save non-JSON responses")
		}
		if dec.Decode(new(any)) != io.EOF {
			return fmt.Errorf("response contains trailing data; use --output to save raw responses")
		}
	} else if !include {
		return nil
	}
	if include {
		value = map[string]any{"status": resp.StatusCode, "headers": resp.Header, "body": value}
	}
	return apiStructuredOutput(cmd, value)
}

// Headers may express operation-specific preconditions (If-Match) and webhook
// signatures, but cannot replace the client's identity, scope, or HTTP framing.
func apiRequestHeaders(pairs []string) (http.Header, error) {
	headers := http.Header{}
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			return nil, apiValidation("--header requires name=value")
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return nil, apiValidation("invalid header name")
			}
		}
		for _, c := range value {
			if c < 32 && c != '\t' || c == 127 {
				return nil, apiValidation("invalid header value")
			}
		}
		name = http.CanonicalHeaderKey(name)
		switch name {
		case "Authorization", "Cookie", "Host", "X-Workspace-Id", "Content-Type", "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "Upgrade", "Te", "Keep-Alive", "Idempotency-Key":
			return nil, apiValidation("--header cannot override managed authentication, workspace, body, idempotency, or transport headers")
		}
		if strings.HasPrefix(name, "Proxy-") {
			return nil, apiValidation("--header cannot set proxy headers")
		}
		headers.Add(name, value)
	}
	return headers, nil
}
