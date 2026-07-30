package api

// Instance security posture (#1379).
//
// Instance-level security is entirely env-driven and, by design, NOT flippable
// from the app — these are deploy decisions, not workspace settings. But their
// *state* was invisible: "are we storing credentials in plaintext? is signup
// open? is the rate limiter off?" had no answer short of SSHing to the box and
// reading the process environment. That is a bad place to keep a security
// posture, because the person who needs it (an admin triaging an incident) is
// often exactly the person who can't get a shell.
//
// This endpoint answers those questions and nothing else. It reports booleans
// and enum-ish state ONLY — never a secret, never a key, never a client secret,
// not even a redacted one. "Configured / not configured" is the whole contract,
// so there is no value here worth stealing and no reason to gate it beyond the
// admin role the rest of the admin surface already uses.
//
// Every field reads the SAME accessor the runtime enforces with, rather than
// re-parsing the environment locally. A posture that parses a flag its own way
// eventually disagrees with the code that acts on it, and a security report
// that confidently states the wrong thing is worse than no report.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/secrets"
)

// SecurityPostureHandler serves the read-only instance posture.
type SecurityPostureHandler struct {
	// allowSignup mirrors cfg.Auth.AllowSignup, threaded in at construction
	// because config isn't reachable from the handler layer.
	allowSignup bool
	// oauthConfigured mirrors "cfg.Auth.GoogleClientID and GoogleSecret are
	// both set" — presence only; neither value is stored here.
	oauthConfigured bool
	// db backs the state probes. Nil is tolerated (embedded/test harnesses):
	// the env-derived half of the posture still renders.
	db     *sql.DB
	logger *slog.Logger
}

// NewSecurityPostureHandler builds the handler from the resolved config values
// the server already holds. Booleans are taken at construction so the handler
// never touches the config struct (and therefore can never accidentally
// serialize a secret out of it).
func NewSecurityPostureHandler(allowSignup, oauthConfigured bool, db *sql.DB, logger *slog.Logger) *SecurityPostureHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SecurityPostureHandler{
		allowSignup: allowSignup, oauthConfigured: oauthConfigured, db: db, logger: logger,
	}
}

// postureWarning is a derived, human-readable risk note. The raw booleans say
// what is set; a warning says why it matters and what it costs — which is the
// part an operator actually needs at 2am.
type postureWarning struct {
	// Key is stable and machine-matchable so a script can gate on it.
	Key      string `json:"key"`
	Severity string `json:"severity"` // "high" | "medium" | "info"
	Message  string `json:"message"`
}

type securityPostureResponse struct {
	// Environment is CREWSHIP_ENV as the process sees it ("prod",
	// "production", "dev", …, or "" when unset). Several guards key off it,
	// so an operator debugging "why is the limiter still on?" needs to see
	// what the process actually thinks it is.
	Environment string `json:"environment"`

	// Encryption — the highest-signal pair on this page.
	EncryptionKeyConfigured bool `json:"encryption_key_configured"`
	PlaintextSecretsAllowed bool `json:"plaintext_secrets_allowed"`

	// Egress ceiling. This is the instance half of the two-key private-egress
	// opt-in; the crew half is per-crew allow_private_endpoints.
	PrivateEndpointsCeiling bool `json:"private_endpoints_ceiling"`

	// Auth surface.
	SignupOpen      bool `json:"signup_open"`
	OAuthConfigured bool `json:"oauth_configured"`
	EmailConfigured bool `json:"email_configured"`

	// Rate limiting. Disabled is what the operator asked for;
	// EffectivelyDisabled is what they got — in prod the limiter runs
	// regardless, and conflating the two hides a real misconfiguration.
	RateLimitDisabled            bool `json:"rate_limit_disabled"`
	RateLimitEffectivelyDisabled bool `json:"rate_limit_effectively_disabled"`

	Warnings []postureWarning `json:"warnings"`
}

// isProdEnv mirrors the production test the rate-limit guard uses, so the
// posture's notion of "prod" cannot drift from the one that keeps the limiter
// on.
func isProdEnv(env string) bool {
	e := strings.ToLower(strings.TrimSpace(env))
	return e == "prod" || e == "production"
}

// buildSecurityPosture assembles the report. Split out from the HTTP handler so
// the derivation (especially the warning rules) is testable without a request.
//
// rateLimitOff is passed in rather than read from the package-level
// rateLimitDisabled: that var is resolved once at init from the environment, so
// a test could never exercise the prod-override branch — and a branch no test
// can reach is one nobody notices breaking.
// postureState is what the instance has BECOME, as opposed to how it was
// started. The env flags answer "how was this deployed"; these answer "what
// has happened to it since" — a boundary switched off in the UI months later,
// a demo account nobody removed, a backup nobody ever took. An instance can
// be flawlessly configured at deploy and still fail every one of these.
type postureState struct {
	// EncryptionKeySource is "generated" when Crewship bootstrapped the master
	// key itself, "external" when the operator supplied it.
	EncryptionKeySource string
	// PrivilegedCredentialWorkspaces have allow_privileged_credentials on,
	// which removes the fail-closed boundary between privileged crews and
	// stored secrets (#1032).
	PrivilegedCredentialWorkspaces int
	// PrivateEndpointCrews have opted into reaching RFC1918/loopback. Only
	// meaningful when the instance ceiling is also open.
	PrivateEndpointCrews int
	// SeedAccountDefaultPassword is true when the demo account created by
	// `crewship seed` still authenticates with the password printed in the
	// docs.
	SeedAccountDefaultPassword bool
	// BackupsRecorded counts backup actions in the audit trail. Zero means
	// this instance has never been backed up.
	BackupsRecorded int
	// BackupsRecordedKnown is false when that COUNT errored. It matters
	// because this is the one probe whose *finding* fires on zero: without
	// it, a failed query is indistinguishable from a never-backed-up
	// instance and the panel invents a warning out of a database error.
	// The other probes only warn on a non-zero count, so a failed probe
	// there under-reports rather than fabricates.
	BackupsRecordedKnown bool
}

func buildSecurityPosture(allowSignup, oauthConfigured, emailConfigured, rateLimitOff bool, st postureState) securityPostureResponse {
	env := os.Getenv("CREWSHIP_ENV")
	prod := isProdEnv(env)

	p := securityPostureResponse{
		Environment:             env,
		EncryptionKeyConfigured: encryption.KeyConfigured(),
		PlaintextSecretsAllowed: encryption.PlaintextSecretsAllowed(),
		PrivateEndpointsCeiling: orchestrator.InstanceAllowsPrivateEndpoints(),
		SignupOpen:              allowSignup,
		OAuthConfigured:         oauthConfigured,
		EmailConfigured:         emailConfigured,
		RateLimitDisabled:       rateLimitOff,
		// In prod the limiter runs no matter what the flag says
		// (MustNotDisableRateLimitInProd), so the *effective* state is what an
		// operator should act on.
		RateLimitEffectivelyDisabled: rateLimitOff && !prod,
		Warnings:                     []postureWarning{},
	}

	// --- Derived warnings, worst first ----------------------------------
	//
	// Each one names the consequence, not just the flag. "plaintext_secrets is
	// on" is a fact; "every credential in this instance is readable by anyone
	// with the DB file" is the thing that gets acted on.

	if p.PlaintextSecretsAllowed {
		sev := "high"
		msg := "Credentials may be stored UNENCRYPTED at rest: " +
			encryption.AllowPlaintextSecretsEnvVar + " is set. Anyone with the database file can read every stored secret."
		if prod {
			msg = "PRODUCTION instance is running with " + encryption.AllowPlaintextSecretsEnvVar +
				" set — credentials may be stored unencrypted at rest and are readable by anyone with the database file."
		}
		p.Warnings = append(p.Warnings, postureWarning{Key: "plaintext_secrets_allowed", Severity: sev, Message: msg})
	}

	if !p.EncryptionKeyConfigured {
		// Distinct from the flag above: no key means secret WRITES fail closed
		// unless the plaintext opt-out is set. Together they are the
		// silently-storing-plaintext combination.
		sev := "medium"
		msg := "No ENCRYPTION_KEY is configured. Secret writes fail closed; set one (openssl rand -hex 32) to store credentials encrypted."
		if p.PlaintextSecretsAllowed {
			sev = "high"
			msg = "No ENCRYPTION_KEY is configured AND plaintext secrets are explicitly allowed — new credentials are being written in the clear."
		}
		p.Warnings = append(p.Warnings, postureWarning{Key: "encryption_key_missing", Severity: sev, Message: msg})
	}

	if p.RateLimitEffectivelyDisabled {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "rate_limit_disabled", Severity: "medium",
			// Scope matters here. The flag short-circuits
			// RateLimiter.Middleware, which is the per-IP HTTP group and
			// nothing else: the login lockout lives in the signin handler
			// (checkAndLockoutOnFail) and never reads this flag, and neither
			// do the notification, provisioning or webhook limiters. Saying
			// "credential stuffing is unthrottled" is both wrong and
			// self-defeating — an operator who knows the lockout still works
			// learns to discount this panel, and then ignores a real warning
			// on it later.
			Message: "The per-IP HTTP rate limits are OFF, so /credentials/test and " +
				"/credentials/{id}/reveal can be hammered from one address. The login " +
				"lockout, notification, provisioning and webhook limits are unaffected " +
				"and still apply.",
		})
	} else if p.RateLimitDisabled && prod {
		// Not a vulnerability — the guard held — but it means someone's intent
		// and the running state disagree, which is worth knowing before the
		// next deploy moves this config somewhere the guard doesn't apply.
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "rate_limit_disabled_ignored_in_prod", Severity: "info",
			Message: "A rate-limit disable flag is set but IGNORED because this is a production environment. The limiter is running.",
		})
	}

	if p.SignupOpen {
		sev := "medium"
		if prod {
			sev = "high"
		}
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "signup_open", Severity: sev,
			Message: "Open signup is enabled — anyone who can reach this instance can create an account.",
		})
	}

	if p.PrivateEndpointsCeiling {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "private_endpoints_ceiling_open", Severity: "info",
			Message: "The instance ceiling for private-network egress is OPEN. Crews that also set allow_private_endpoints can reach RFC1918/loopback addresses. Link-local and cloud-metadata stay blocked regardless.",
		})
	}

	// ── State-derived findings ──────────────────────────────────────────

	// A key Crewship generated for itself lives in <dataDir>/secrets.env,
	// beside the database it protects. That is fine on a laptop and wrong on
	// anything whose disk gets snapshotted, backed up or moved: the copy
	// carries the ciphertext AND what opens it.
	if st.EncryptionKeySource == "generated" {
		sev := "medium"
		if prod {
			sev = "high"
		}
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "encryption_key_generated", Severity: sev,
			Message: "The master key was generated by Crewship and is stored beside the database " +
				"(<data-dir>/secrets.env). Any copy of that volume — a snapshot, a backup, a moved " +
				"disk — carries both the encrypted secrets and the key that opens them. Supply " +
				"ENCRYPTION_KEY from the environment instead.",
		})
	}

	// The switch that lets privileged (Docker-in-Docker) crews receive
	// credentials. Under --privileged the UID 1001/1002 split is gone, so any
	// process in that container can read the CredStore.
	if st.PrivilegedCredentialWorkspaces > 0 {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "privileged_credentials_enabled", Severity: "high",
			Message: fmt.Sprintf("%d workspace(s) allow credentials in PRIVILEGED crews. In those crews the "+
				"container-isolation boundary does not apply: every process inside can read every "+
				"credential delivered to it. Turn it off in Settings → General unless a Docker-in-Docker "+
				"workload genuinely needs vaulted secrets.", st.PrivilegedCredentialWorkspaces),
		})
	}

	// Pairs with the ceiling warning above: the ceiling being open is a
	// posture note, crews actually crossing it is a finding.
	if p.PrivateEndpointsCeiling && st.PrivateEndpointCrews > 0 {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "private_endpoints_in_use", Severity: "medium",
			Message: fmt.Sprintf("%d crew(s) reach private-network addresses through the open instance "+
				"ceiling. Agents in them can call RFC1918/loopback services — internal APIs, databases, "+
				"anything on the host network.", st.PrivateEndpointCrews),
		})
	}

	// The seed password is printed in the docs and in CLAUDE.md. An instance
	// still accepting it is one search away from anyone.
	if st.SeedAccountDefaultPassword {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "seed_account_default_password", Severity: "high",
			Message: "The demo account created by `crewship seed` still authenticates with the password " +
				"published in the documentation. Change it or remove the account.",
		})
	}

	// Not a vulnerability — a recoverability one, and the panel an operator
	// checks before an incident is the right place to learn it.
	if st.BackupsRecordedKnown && st.BackupsRecorded == 0 {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "no_backup_recorded", Severity: "medium",
			Message: "No backup has ever been recorded on this instance. The database holds every " +
				"credential, memory and audit record; `crewship backup create` is the whole recovery story.",
		})
	}

	// Worst first. A high-severity row sitting under three notes is a row
	// that gets skimmed past.
	sort.SliceStable(p.Warnings, func(i, j int) bool {
		return severityRank(p.Warnings[i].Severity) < severityRank(p.Warnings[j].Severity)
	})

	return p
}

// severityRank orders the panel. Unknown severities sort last rather than
// first: a typo should not push a real finding down the list.
func severityRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	case "info":
		return 3
	default:
		return 4
	}
}

// seedAccountEmail / seedAccountPassword are what `crewship seed` creates when
// the operator passes no --password. Both are printed in the docs, so an
// instance that still accepts them is one search away from anyone.
const (
	seedAccountEmail    = "demo@crewship.ai"
	seedAccountPassword = "password123" //gitleaks:allow — the documented dev default, checked FOR, never used
)

// readPostureState probes what the instance has become. Every query is a
// bounded COUNT against an indexed column; the one expensive step (a bcrypt
// compare) runs only when the seed account exists at all.
//
// A failed probe leaves its field at the zero value and does NOT fail the
// request: a posture panel that refuses to render because one COUNT errored
// is worse than one that reports five findings out of six. Errors are logged.
func readPostureState(ctx context.Context, db *sql.DB, logger *slog.Logger) postureState {
	st := postureState{EncryptionKeySource: secrets.EncryptionKeySource()}
	if db == nil {
		return st
	}

	probe := func(what, query string, dest *int) bool {
		if err := db.QueryRowContext(ctx, query).Scan(dest); err != nil {
			logger.Warn("security posture probe failed", "probe", what, "error", err)
			return false
		}
		return true
	}
	probe("privileged_credentials",
		`SELECT COUNT(*) FROM workspaces WHERE allow_privileged_credentials = 1 AND deleted_at IS NULL`,
		&st.PrivilegedCredentialWorkspaces)
	probe("private_endpoint_crews",
		`SELECT COUNT(*) FROM crews WHERE allow_private_endpoints = 1 AND deleted_at IS NULL`,
		&st.PrivateEndpointCrews)
	st.BackupsRecordedKnown = probe("backups_recorded",
		`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'backup.%'`,
		&st.BackupsRecorded)

	// Does the documented demo login still work? Checking the hash is the
	// difference between "an account called demo@ exists", which may be
	// perfectly legitimate, and "the published password opens this instance",
	// which never is.
	var hash sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT hashed_password FROM users WHERE email = ? AND deleted_at IS NULL`,
		seedAccountEmail).Scan(&hash)
	switch {
	case err == sql.ErrNoRows:
		// No demo account — nothing to say.
	case err != nil:
		logger.Warn("security posture probe failed", "probe", "seed_account", "error", err)
	case hash.Valid && hash.String != "":
		st.SeedAccountDefaultPassword =
			bcrypt.CompareHashAndPassword([]byte(hash.String), []byte(seedAccountPassword)) == nil
	}

	return st
}

// Get handles GET /api/v1/admin/security-posture.
//
// Admin-gated like the rest of the admin surface. The response deliberately
// contains no secret material at all — only whether things are configured — so
// the gate is about who should be reasoning about instance posture, not about
// protecting a payload.
func (h *SecurityPostureHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	// Read the mailer's own answer rather than re-testing RESEND_* here — it
	// is the transport that knows whether it can actually send.
	emailConfigured := mailer.NewFromEnv().Configured()
	writeJSON(w, http.StatusOK, buildSecurityPosture(
		h.allowSignup, h.oauthConfigured, emailConfigured, rateLimitDisabled,
		readPostureState(r.Context(), h.db, h.logger)))
}
