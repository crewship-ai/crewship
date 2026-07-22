package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// nonInteractiveLoginServer stands in for the real server across the three
// requests a successful `crewship login` makes: the NextAuth CSRF+credentials
// handshake (exchangeCredentialsForSession) followed by the CLI-token mint.
// Plain HTTP, so the cookie is named authjs.csrf-token / authjs.session-token
// (see exchangeCredentialsForSession's HTTPS vs HTTP naming split, already
// covered by cmd_login_https_test.go — this server exercises the leg after
// that: minting and persisting a token with zero TTY interaction).
func nonInteractiveLoginServer(t *testing.T, wantPassword, mintedToken string) *httptest.Server {
	t.Helper()
	const csrfSecret = "csrf-secret-nonint"
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/csrf", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "authjs.csrf-token", Value: csrfSecret, Path: "/"})
		replyJSON(w, http.StatusOK, map[string]string{"csrfToken": csrfSecret})
	})

	mux.HandleFunc("/api/auth/callback/credentials", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var in struct {
			Password  string `json:"password"`
			CSRFToken string `json:"csrfToken"`
		}
		_ = json.Unmarshal(body, &in)
		if in.CSRFToken != csrfSecret || in.Password != wantPassword {
			replyJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "CredentialsSignin"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "authjs.session-token", Value: "session-xyz", Path: "/"})
		replyJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	})

	mux.HandleFunc("/api/v1/auth/cli-token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-xyz" {
			replyJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		replyJSON(w, http.StatusOK, map[string]string{"token": mintedToken})
	})

	return httptest.NewServer(mux)
}

// ─── resolveLoginPassword — unit coverage of the three-way priority ────

func TestResolveLoginPassword_EnvVarWins(t *testing.T) {
	t.Setenv("CREWSHIP_PASSWORD", "envpw123")
	pw, err := resolveLoginPassword(bufio.NewReader(strings.NewReader("")))
	if err != nil || pw != "envpw123" {
		t.Errorf("pw=%q err=%v", pw, err)
	}
}

func TestResolveLoginPassword_StdinFlag(t *testing.T) {
	old := loginPasswordStdinFlag
	loginPasswordStdinFlag = true
	t.Cleanup(func() { loginPasswordStdinFlag = old })

	pw, err := resolveLoginPassword(bufio.NewReader(strings.NewReader("hunter2\n")))
	if err != nil || pw != "hunter2" {
		t.Errorf("pw=%q err=%v", pw, err)
	}
}

func TestResolveLoginPassword_StdinFlagEmpty(t *testing.T) {
	old := loginPasswordStdinFlag
	loginPasswordStdinFlag = true
	t.Cleanup(func() { loginPasswordStdinFlag = old })

	_, err := resolveLoginPassword(bufio.NewReader(strings.NewReader("   \n")))
	if err == nil || !strings.Contains(err.Error(), "no password on stdin") {
		t.Errorf("got %v", err)
	}
}

func TestResolveLoginPassword_EnvAndStdinFlagMutuallyExclusive(t *testing.T) {
	old := loginPasswordStdinFlag
	loginPasswordStdinFlag = true
	t.Cleanup(func() { loginPasswordStdinFlag = old })
	t.Setenv("CREWSHIP_PASSWORD", "envpw")

	_, err := resolveLoginPassword(bufio.NewReader(strings.NewReader("stdin-pw\n")))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("got %v", err)
	}
}

// ─── loginInteractive — end-to-end, driven exactly as a systemd unit /
// CI job / test harness would drive it: zero TTY, --email + one of the two
// non-interactive password sources. This is the acceptance test for #1331:
// pre-fix, loginInteractive was untestable this way at all (term.ReadPassword
// requires a real TTY and errors outright over a pipe — see
// TestLoginInteractive_PasswordReadFailsOffTTY in cmd_login_cov_test.go,
// which pins that exact failure for the still-interactive default path).

func TestLoginInteractive_PasswordStdinEndToEnd(t *testing.T) {
	saveCLIState(t)
	tempCLIConfig(t)

	srv := nonInteractiveLoginServer(t, "s3cr3t-pw", "cli-tok-final")
	defer srv.Close()

	oldEmail, oldStdinFlag := loginEmailFlag, loginPasswordStdinFlag
	loginEmailFlag = "demo@crewship.ai"
	loginPasswordStdinFlag = true
	t.Cleanup(func() { loginEmailFlag, loginPasswordStdinFlag = oldEmail, oldStdinFlag })

	swapStdin(t, "s3cr3t-pw\n")

	out := captureStdoutCovCli2(t, func() {
		if err := loginInteractive(srv.URL); err != nil {
			t.Fatalf("loginInteractive: %v", err)
		}
	})

	if strings.Contains(out, "Email:") {
		t.Errorf("--email must skip the Email: prompt entirely, got:\n%s", out)
	}
	if !strings.Contains(out, "Login successful!") {
		t.Errorf("missing success message:\n%s", out)
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Token != "cli-tok-final" || cfg.Server != srv.URL {
		t.Errorf("token not persisted correctly: %+v", cfg)
	}
}

func TestLoginInteractive_PasswordEnvVarEndToEnd(t *testing.T) {
	saveCLIState(t)
	tempCLIConfig(t)

	srv := nonInteractiveLoginServer(t, "envpw-secret", "cli-tok-env")
	defer srv.Close()

	oldEmail := loginEmailFlag
	loginEmailFlag = "demo@crewship.ai"
	t.Cleanup(func() { loginEmailFlag = oldEmail })
	t.Setenv("CREWSHIP_PASSWORD", "envpw-secret")

	// Zero stdin needed at all with --email + CREWSHIP_PASSWORD — the
	// systemd-unit shape: no piping, just env + flags.
	swapStdin(t, "")

	out := captureStdoutCovCli2(t, func() {
		if err := loginInteractive(srv.URL); err != nil {
			t.Fatalf("loginInteractive: %v", err)
		}
	})
	if !strings.Contains(out, "Login successful!") {
		t.Errorf("missing success message:\n%s", out)
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Token != "cli-tok-env" || cfg.Server != srv.URL {
		t.Errorf("token not persisted correctly: %+v", cfg)
	}
}

func TestLoginInteractive_WrongPasswordStdin(t *testing.T) {
	saveCLIState(t)
	tempCLIConfig(t)

	srv := nonInteractiveLoginServer(t, "correct-pw", "cli-tok-unused")
	defer srv.Close()

	oldEmail, oldStdinFlag := loginEmailFlag, loginPasswordStdinFlag
	loginEmailFlag = "demo@crewship.ai"
	loginPasswordStdinFlag = true
	t.Cleanup(func() { loginEmailFlag, loginPasswordStdinFlag = oldEmail, oldStdinFlag })

	swapStdin(t, "wrong-pw\n")

	_ = captureStdoutCovCli2(t, func() {
		err := loginInteractive(srv.URL)
		if err == nil {
			t.Fatal("expected an error for a wrong password")
		}
		if !strings.Contains(err.Error(), "invalid email or password") {
			t.Errorf("got %v", err)
		}
	})
}
