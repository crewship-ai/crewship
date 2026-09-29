//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"time"
)

// brokerStream forwards a bounded stream only under an explicit v2 grant.
// Every transport frame has a fresh authority decision. The runtime watchdog
// also cancels an idle upstream request if renewal fails while Read is blocked.
func (s *Session) brokerStream(ctx context.Context, response *http.Response, grant HTTPGrant, secret BoundSecret, emit func(brokerFrame) error) brokerFrame {
	failure := brokerFrame{Kind: "response", Status: 502}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/event-stream" || response.StatusCode != http.StatusOK || response.Header.Get("Upgrade") != "" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || response.ContentLength > grant.MaxResponse {
		return failure
	}
	authorized := func() bool {
		if ctx.Err() != nil || (secret.Value != "" && !secret.Expires.After(time.Now())) || s.brokerAuthorized(ctx) != nil {
			go s.Stop("broker_stream_authority_denied")
			return false
		}
		return true
	}
	if !authorized() {
		return brokerFrame{Kind: "response", Status: 403}
	}
	if emit(brokerFrame{Kind: "stream_start", Status: 200}) != nil {
		return brokerFrame{Kind: "stream_error", Status: 502}
	}
	failure.Kind = "stream_error"
	var received, delivered int64
	var pending []byte
	redact := []byte(secret.Value)
	prefix := secretPrefixTable(redact)
	send := func(data []byte) bool {
		if len(data) == 0 {
			return true
		}
		delivered += int64(len(data))
		return delivered <= grant.MaxResponse && authorized() && emit(brokerFrame{Kind: "stream_chunk", Body: data}) == nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buf)
		received += int64(n)
		if received > grant.MaxResponse || !authorized() {
			return failure
		}
		pending = append(pending, buf[:n]...)
		// Withhold a suffix that may be a literal secret split across reads.
		// Determine the suffix after full-value replacement: otherwise a secret
		// whose suffix is also its prefix could be partially released.
		if len(redact) != 0 {
			pending = bytes.ReplaceAll(pending, redact, []byte("[REDACTED]"))
		}
		keep := secretSuffixLength(pending, redact, prefix)
		if !send(pending[:len(pending)-keep]) {
			return failure
		}
		pending = bytes.Clone(pending[len(pending)-keep:])
		if readErr != nil {
			if readErr != io.EOF {
				return failure
			}
			if len(pending) != 0 && !send([]byte("[REDACTED_PARTIAL]")) {
				return failure
			}
			if !authorized() {
				return failure
			}
			return brokerFrame{Kind: "stream_end", Status: 200}
		}
	}
}

// Linear-time prefix matching bounds host CPU even when a large credential and
// upstream chunks contain long repetitive prefixes.
func secretPrefixTable(secret []byte) []int {
	prefix := make([]int, len(secret))
	for i, matched := 1, 0; i < len(secret); i++ {
		for matched > 0 && secret[i] != secret[matched] {
			matched = prefix[matched-1]
		}
		if secret[i] == secret[matched] {
			matched++
		}
		prefix[i] = matched
	}
	return prefix
}

func secretSuffixLength(data, secret []byte, prefix []int) int {
	if len(secret) == 0 {
		return 0
	}
	matched := 0
	for _, b := range data {
		for matched > 0 && b != secret[matched] {
			matched = prefix[matched-1]
		}
		if b == secret[matched] {
			matched++
		}
		if matched == len(secret) {
			matched = prefix[matched-1]
		}
	}
	return matched
}
