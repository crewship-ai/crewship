//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
)

func (s *Session) brokerNativeStream(ctx context.Context, response *http.Response, grant HTTPGrant, secret BoundSecret, native NativeBrokerAuthority, ticket string, emit func(brokerFrame) error) (brokerFrame, BrokerUsage) {
	failure := brokerFrame{Kind: "response", Status: 502}
	contentType, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if e != nil || contentType != "text/event-stream" || response.StatusCode != http.StatusOK || response.Header.Get("Upgrade") != "" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || response.ContentLength > grant.MaxResponse {
		return failure, BrokerUsage{}
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, grant.MaxResponse+1))
	if e != nil || int64(len(raw)) > grant.MaxResponse || (secret.Value != "" && bytes.Contains(raw, []byte(secret.Value))) || s.brokerAuthorized(ctx) != nil {
		return failure, BrokerUsage{}
	}
	issued, stream, e := NativeTerminal(raw, grant.Native.Model)
	if e != nil || int64(len(stream)) > grant.MaxResponse {
		return failure, BrokerUsage{}
	}
	if e = native.NativeComplete(ctx, s.handle, ticket, issued); e != nil {
		return failure, BrokerUsage{}
	}
	// No upstream delta or tool argument reaches the worker before the complete
	// response passes validation and durable attempt-bound provenance is recorded.
	clean := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, ContentLength: int64(len(stream)), Body: io.NopCloser(bytes.NewReader(stream))}
	result := s.brokerStream(ctx, clean, grant, secret, emit)
	if result.Kind != "stream_end" || result.Status != 200 {
		return result, BrokerUsage{}
	}
	return result, terminalResponsesUsage(raw, grant.Native.Model)
}
