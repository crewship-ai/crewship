//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
)

// InstallAcceptanceTLS is compiled only into the explicit live acceptance
// harness, never release binaries. It redirects the already validated fixed
// upstream operation to an owned TLS fixture, preserving provider credentials,
// closed-schema validation, runtime identity and real accounting callbacks.
func InstallAcceptanceTLS(m *Manager, server *httptest.Server) error {
	if m == nil || server == nil || server.Listener == nil {
		return ErrDenied
	}
	addr, ok := server.Listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return ErrDenied
	}
	clientTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok || clientTransport.TLSClientConfig == nil {
		return ErrDenied
	}
	tr := defaultBrokerTransport()
	tr.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	tr.locals = func() ([]net.Addr, error) { return nil, nil }
	tr.tls = clientTransport.TLSClientConfig.Clone()
	tr.tls.ServerName = "example.com"
	tr.dial = func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" || target != "8.8.8.8:443" {
			return nil, ErrDenied
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	m.brokerTransport = &tr
	return nil
}
