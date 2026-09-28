//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type brokerProcess struct {
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	done   chan error
	once   sync.Once
}

func (b *brokerProcess) close() {
	b.once.Do(func() { b.cancel(); _ = b.stdin.Close(); _ = b.stdout.Close() })
}

// These seams are private and exist only for synthetic endpoint acceptance.
// The public constructor never accepts a relaxed resolver or transport.
type brokerTransport struct {
	lookup func(context.Context, string) ([]net.IPAddr, error)
	locals func() ([]net.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	tls    *tls.Config
}

func defaultBrokerTransport() brokerTransport {
	return brokerTransport{lookup: net.DefaultResolver.LookupIPAddr, locals: net.InterfaceAddrs, dial: (&net.Dialer{Timeout: 3 * time.Second}).DialContext}
}

func (s *Session) startBroker(ctx context.Context) (string, error) {
	if _, ok := s.manager.Authority.(BrokerAuthority); !ok {
		return "", ErrDenied
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(nonce[:])
	owned, cancel := context.WithCancel(context.Background())
	cmd := s.manager.Docker.command(owned, "exec", "-i", "--user", "1002:1002", s.id, "/opt/crewship-runner", "broker")
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return "", err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = in.Close()
		return "", err
	}
	b := &brokerProcess{cancel: cancel, stdin: in, stdout: out, cmd: cmd, done: make(chan error, 1)}
	if err = cmd.Start(); err != nil {
		b.close()
		return "", ErrDenied
	}
	s.broker = b
	go func() { err := cmd.Wait(); b.close(); b.done <- err; close(b.done) }()
	limits := map[string]int64{}
	for _, g := range s.plan.Network.Grants {
		limits[g.ID] = g.MaxRequest
	}
	ready := make(chan error, 1)
	go func() {
		err := writeBrokerFrame(in, brokerFrame{Kind: "configure", Token: token, Limits: limits})
		if err == nil {
			var f brokerFrame
			err = readBrokerFrame(out, &f)
			if err == nil && f.Kind != "ready" {
				err = ErrDenied
			}
		}
		ready <- err
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err = <-ready:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = ErrDenied
	}
	if err != nil {
		b.close()
		return "", ErrDenied
	}
	tr := defaultBrokerTransport()
	if s.manager.brokerTransport != nil {
		tr = *s.manager.brokerTransport
	}
	go func() {
		defer b.close()
		for {
			var req brokerFrame
			if readBrokerFrame(out, &req) != nil {
				return
			}
			if req.Kind != "request" {
				return
			}
			response := s.brokerRequest(owned, token, req, tr)
			if writeBrokerFrame(in, response) != nil {
				return
			}
		}
	}()
	return token, nil
}

func (s *Session) brokerAuthorized(ctx context.Context) error {
	r := s.Record()
	if r.Status != "running" || !time.Now().Before(r.Expires) {
		return ErrDenied
	}
	p, err := resolve(ctx, s.manager.Authority, s.handle, map[string]bool{})
	if err != nil || p.fingerprint() != s.plan.fingerprint() {
		return ErrDenied
	}
	return nil
}

func (s *Session) brokerRequest(parent context.Context, token string, req brokerFrame, tr brokerTransport) brokerFrame {
	denied := brokerFrame{Kind: "response", Status: 403}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 {
		return denied
	}
	var grant *HTTPGrant
	for i := range s.plan.Network.Grants {
		if s.plan.Network.Grants[i].ID == req.Operation {
			grant = &s.plan.Network.Grants[i]
			break
		}
	}
	if grant == nil || int64(len(req.Body)) > grant.MaxRequest {
		return denied
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(grant.TimeoutMillis)*time.Millisecond)
	defer cancel()
	if s.brokerAuthorized(ctx) != nil {
		go s.Stop("broker_authority_denied")
		return denied
	}
	var secret BoundSecret
	if grant.CredentialID != "" {
		a, ok := s.manager.Authority.(BrokerAuthority)
		if !ok {
			return denied
		}
		var err error
		secret, err = a.BrokerSecret(ctx, s.handle, grant.CredentialID)
		if err != nil || secret.Value == "" || len(secret.Value) > 64<<10 || !utf8.ValidString(secret.Value) || strings.ContainsAny(secret.Value, "\r\n\x00") || !secret.Expires.After(time.Now()) {
			return denied
		}
		matched := false
		for _, c := range s.plan.Network.Credentials {
			if c.ID == grant.CredentialID && c.ID == secret.ID && c.Revision == secret.Revision && c.Provider == secret.Provider && c.Account == secret.Account {
				matched = true
			}
		}
		if !matched {
			return denied
		}
		// A broker response can echo a credential; suppress literal values before
		// returning it. This does not classify or prevent encoded disclosure.
	}
	u, err := url.Parse(grant.URL)
	if err != nil {
		return denied
	}
	ip, err := resolveBrokerIP(ctx, u.Hostname(), tr.lookup, tr.locals)
	if err != nil {
		return denied
	}
	// DNS and credential resolution may take time: order final admission after both.
	if s.brokerAuthorized(ctx) != nil {
		go s.Stop("broker_authority_denied")
		return denied
	}
	if secret.Value != "" && !secret.Expires.After(time.Now()) {
		return denied
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	transport := &http.Transport{
		DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second,
		TLSClientConfig: tr.tls,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tr.dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, grant.Method, grant.URL, bytes.NewReader(req.Body))
	if err != nil {
		return denied
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	if secret.Value != "" {
		request.Header.Set("Authorization", "Bearer "+secret.Value)
	}
	// Disable implicit replay even for idempotent operations.
	request.GetBody = nil
	response, err := client.Do(request)
	if err != nil {
		return brokerFrame{Kind: "response", Status: 502}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") || response.Header.Get("Upgrade") != "" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return brokerFrame{Kind: "response", Status: 502}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, grant.MaxResponse+1))
	if err != nil || int64(len(body)) > grant.MaxResponse {
		return brokerFrame{Kind: "response", Status: 502}
	}
	if s.brokerAuthorized(ctx) != nil {
		go s.Stop("broker_authority_denied")
		return denied
	}
	if secret.Value != "" {
		body = bytes.ReplaceAll(body, []byte(secret.Value), []byte("[REDACTED]"))
	}
	if int64(len(body)) > grant.MaxResponse {
		return brokerFrame{Kind: "response", Status: 502}
	}
	return brokerFrame{Kind: "response", Status: response.StatusCode, Body: body}
}
