package sidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

type authenticatedLLMRunKey struct{}

// Host IPC is a trusted configured destination, not user input. Never use an
// ambient proxy or follow a redirect carrying the crew token or account data.
var bedrockAuthorityClient = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// peerCredentialPatterns bounds redaction registrations across renewals. It
// retains hashes only; account documents live for one upstream request.
type peerCredentialPatterns struct {
	sync.Mutex
	seen map[[32]byte]bool
}

func (s *Server) resolveBedrock(r *http.Request, actorID, accountID, model string) (*Credential, error) {
	runID, _ := r.Context().Value(authenticatedLLMRunKey{}).(string)
	if s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.Token == "" || s.ipc.CrewID == "" || runID == "" || actorID == "" || accountID == "" {
		return nil, errors.New("Bedrock account authority unavailable")
	}
	target, err := url.Parse(s.ipc.BaseURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("invalid Bedrock authority")
	}
	target.Path = bedrock.ResolvePath
	raw, _ := json.Marshal(bedrock.ResolveRequest{RunID: runID, AgentID: actorID, AccountID: accountID, Model: model})
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("Bedrock authority request unavailable")
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := bedrockAuthorityClient.Do(req)
	if err != nil {
		return nil, errors.New("Bedrock account authority unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("Bedrock account not authorized")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil || len(data) > 32768 {
		return nil, errors.New("invalid Bedrock authority response")
	}
	var resolved bedrock.ResolveResponse
	if json.Unmarshal(data, &resolved) != nil || resolved.AccountID != accountID {
		return nil, errors.New("invalid Bedrock authority response")
	}
	document, err := json.Marshal(resolved.Account)
	if err != nil {
		return nil, errors.New("invalid Bedrock account")
	}
	account, err := bedrock.Parse(string(document))
	if err != nil || account.Expired(time.Now()) || !account.Allows(model) {
		return nil, errors.New("Bedrock account expired or model not permitted")
	}
	// Register the actual key, not merely the surrounding JSON document.
	digest := sha256.Sum256([]byte(account.APIKey))
	s.peerPatterns.Lock()
	defer s.peerPatterns.Unlock()
	if s.peerPatterns.seen == nil {
		s.peerPatterns.seen = map[[32]byte]bool{}
	}
	if !s.peerPatterns.seen[digest] {
		if len(s.peerPatterns.seen) >= 128 {
			return nil, errors.New("Bedrock redaction capacity reached; restart crew when idle")
		}
		if s.scrubber != nil {
			if err := s.scrubber.AddPattern("bedrock_peer_credential", regexp.QuoteMeta(account.APIKey)); err != nil {
				return nil, errors.New("Bedrock redaction unavailable")
			}
		}
		s.peerPatterns.seen[digest] = true
	}
	return &Credential{ID: accountID, Provider: ProviderType(bedrock.Provider), Token: string(document)}, nil
}

// Shared agent-facing forwarders must never return the secret-only response.
func refuseSecretResolutionForward(w http.ResponseWriter, route string) bool {
	parsed, err := url.Parse(route)
	if err != nil || path.Clean(parsed.Path) == bedrock.ResolvePath {
		http.Error(w, "sidecar-only account transport", http.StatusForbidden)
		return true
	}
	return false
}
