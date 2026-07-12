package api

// Reachability probe for an operator-configured local-model endpoint
// (ENDPOINT_URL credential — Ollama or any OpenAI-compatible server). Used by
// the credential "test" path (U5) and the CLI `doctor` check (U1) so a
// misconfigured or down endpoint surfaces up front instead of as an opaque
// mid-run failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// probeLocalModelEndpoint dials a local-model base URL and reports whether it
// answers with a model list, plus how many models it advertises.
//
// This is a SERVER-SIDE dial of a user-supplied URL, so it is SSRF-aware: the
// dialer refuses link-local / cloud-metadata addresses (169.254.0.0/16 and the
// IPv6 forms) at connect time, after DNS resolution, so a hostname that
// resolves to the metadata endpoint can't pivot. RFC1918/loopback are allowed
// on purpose — a LAN or on-host Ollama is exactly the endpoint an operator
// wants to test, and creating the credential already requires MANAGER+.
func probeLocalModelEndpoint(ctx context.Context, baseURL string) testResult {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return testResult{Error: "empty endpoint URL"}
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				Control: endpointDialControl,
			}).DialContext,
			DisableKeepAlives: true,
		},
		// Never auto-follow a redirect — a 3xx to an internal host must not be
		// chased with the SSRF guard silently re-applied per hop.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// OpenAI-compatible: GET {base}/models -> {"data":[{"id":...}]}.
	if n, ok, err := probeCount(ctx, client, base+"/models", "data"); ok {
		return testResult{Valid: true, Status: http.StatusOK, Error: modelCountNote(n)}
	} else if err != "" && !strings.Contains(err, "status") {
		// Hard transport error (refused/timeout/blocked) — report it; don't
		// bother with the Ollama-native fallback, the host isn't answering.
		return testResult{Error: err}
	}

	// Ollama-native fallback: GET {root}/api/tags -> {"models":[{"name":...}]}.
	if root := strings.TrimSuffix(base, "/v1"); root != base {
		if n, ok, _ := probeCount(ctx, client, root+"/api/tags", "models"); ok {
			return testResult{Valid: true, Status: http.StatusOK, Error: modelCountNote(n)}
		}
	}

	return testResult{Error: "endpoint did not return a model list (tried /models and /api/tags) — check the URL and that the server is running"}
}

func modelCountNote(n int) string {
	if n == 0 {
		return "endpoint reachable, but it advertises no models — pull one first"
	}
	return fmt.Sprintf("endpoint reachable (%d model(s) available)", n)
}

// probeCount GETs url and counts the entries under the given JSON array key.
// Returns (count, ok, errMessage). ok is true only on a 2xx with a decodable
// array; errMessage carries a transport error ("connection refused", etc.) or a
// "status N" note so the caller can decide whether to try a fallback.
func probeCount(ctx context.Context, client *http.Client, url, arrayKey string) (int, bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false, "invalid endpoint URL"
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, "connection failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return 0, false, fmt.Sprintf("status %d", resp.StatusCode)
	}
	// Cap the body so a hostile endpoint can't stream us to death.
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, false, "endpoint returned a non-JSON response"
	}
	raw, ok := payload[arrayKey]
	if !ok {
		return 0, false, "endpoint response missing the model list"
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0, false, "endpoint model list was not an array"
	}
	return len(arr), true, ""
}

// endpointDialControl runs after DNS resolution with the concrete IP in
// address; it refuses link-local / metadata / multicast / unspecified targets
// so a server-side endpoint probe can't be turned into a cloud-metadata SSRF.
func endpointDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // not an IP literal; the resolver already produced concrete IPs on real dials
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
		return fmt.Errorf("endpoint resolves to a link-local/metadata address (%s) — refused", ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("endpoint resolves to a disallowed address (%s) — refused", ip)
	}
	return nil
}
