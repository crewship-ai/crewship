package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/ws"
)

// tokenResponse holds the OAuth token exchange response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func exchangeOAuthCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI, codeVerifier string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second, Transport: ssrfSafeTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}
	return &tokenResp, nil
}

// --- Token Refresh Worker ---

// StartOAuthRefreshWorker runs a background goroutine that refreshes expiring OAuth tokens.
func StartOAuthRefreshWorker(db *sql.DB, hub *ws.Hub, logger *slog.Logger, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Derive a context that cancels when stop is closed
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				refreshExpiringTokens(ctx, db, hub, logger)
			}
		}
	}()
}

func refreshExpiringTokens(ctx context.Context, db *sql.DB, hub *ws.Hub, logger *slog.Logger) {
	// Find tokens expiring within 10 minutes
	threshold := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT id, workspace_id, oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc
		FROM credentials
		WHERE type = 'OAUTH2' AND status = 'ACTIVE'
			AND oauth_token_expires_at != '' AND oauth_token_expires_at < ?
			AND oauth_refresh_token_enc != '' AND deleted_at IS NULL`, threshold)
	if err != nil {
		logger.Error("query expiring OAuth tokens", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, wsID, clientID, clientSecretEnc, tokenURL, refreshTokenEnc string
		if err := rows.Scan(&id, &wsID, &clientID, &clientSecretEnc, &tokenURL, &refreshTokenEnc); err != nil {
			continue
		}

		clientSecret := ""
		if clientSecretEnc != "" {
			d, decErr := encryption.Decrypt(clientSecretEnc)
			if decErr != nil {
				logger.Error("decrypt OAuth client secret during refresh", "credential_id", id, "error", decErr)
				continue
			}
			clientSecret = d
		}
		refreshToken, err := encryption.Decrypt(refreshTokenEnc)
		if err != nil {
			logger.Error("decrypt OAuth refresh token during refresh", "credential_id", id, "error", err)
			if _, dbErr := db.ExecContext(ctx, "UPDATE credentials SET status = 'EXPIRED', updated_at = datetime('now') WHERE id = ?", id); dbErr != nil {
				logger.Error("mark credential expired", "credential_id", id, "error", dbErr)
			}
			continue
		}
		if refreshToken == "" {
			continue
		}

		// Refresh the token
		newToken, err := refreshOAuthToken(ctx, tokenURL, clientID, clientSecret, refreshToken)
		if err != nil {
			logger.Error("OAuth token refresh failed", "credential_id", id, "error", err)
			if _, dbErr := db.ExecContext(ctx, "UPDATE credentials SET status = 'EXPIRED', updated_at = datetime('now') WHERE id = ?", id); dbErr != nil {
				logger.Error("mark credential expired", "credential_id", id, "error", dbErr)
			}
			if hub != nil {
				hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
					Type: "credential.expired", Channel: "workspace:" + wsID,
					Payload: map[string]string{"credential_id": id, "reason": "OAuth token refresh failed"},
				})
			}
			continue
		}

		encAccess, err := encryption.Encrypt(newToken.AccessToken)
		if err != nil {
			logger.Error("encrypt refreshed access token", "credential_id", id, "error", err)
			continue
		}
		expiresAt := ""
		if newToken.ExpiresIn > 0 {
			expiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}

		// Update refresh token only if a new one was issued
		if newToken.RefreshToken != "" {
			encRefresh, err := encryption.Encrypt(newToken.RefreshToken)
			if err != nil {
				logger.Error("encrypt refreshed refresh token", "credential_id", id, "error", err)
				continue
			}
			if _, err := db.ExecContext(ctx, "UPDATE credentials SET encrypted_value = ?, oauth_refresh_token_enc = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, encRefresh, expiresAt, id); err != nil {
				logger.Error("update refreshed tokens", "credential_id", id, "error", err)
				continue
			}
		} else {
			if _, err := db.ExecContext(ctx, "UPDATE credentials SET encrypted_value = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, expiresAt, id); err != nil {
				logger.Error("update refreshed token", "credential_id", id, "error", err)
				continue
			}
		}

		logger.Info("OAuth token refreshed", "credential_id", id)
	}
	if err := rows.Err(); err != nil {
		logger.Error("iterate expiring OAuth tokens", "error", err)
	}
}

func refreshOAuthToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second, Transport: ssrfSafeTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh endpoint returned HTTP %d", resp.StatusCode)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in refresh response")
	}
	return &tokenResp, nil
}
