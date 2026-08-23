package orchestrator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/credpolicy"
	"github.com/crewship-ai/crewship/internal/llmroute"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Where the sidecar's stderr lands, and how much of it we are willing to keep.
//
// /tmp is a tmpfs and tmpfs pages are charged to the crew's memory cgroup, so
// an unbounded log here is a slow memory leak that ends in an OOM kill and
// presents as "the agent died" (crew-runtime-capacity.md §5). 1 MiB is more
// sidecar stderr than any single incident produces; keeping the newest 256 KiB
// on each trim leaves plenty of context for whatever tripped it.
//
// These numbers are load-bearing, not decorative: the cap is what makes this a
// bound rather than a slower leak, and it is charged PER CREW. The target fleet
// is 20–50 crews on one host, so even a 4 MiB cap is ~200 MB of tmpfs across
// the fleet — a cap much larger than that has stopped bounding anything.
// TestSidecarLogCap_StaysInASaneRange pins both ends so a raised constant (or a
// bad merge) cannot quietly restore the unbounded behaviour, and
// TestSidecarLogTrim_* proves the trim against a real 2 MiB file rather than
// against the script's text — the tighter of the two guards.
const (
	sidecarLogPath = "/tmp/sidecar.log"

	sidecarLogMaxBytes  = 1 << 20 // trim once the log passes this
	sidecarLogKeepBytes = 1 << 18 // …down to this much of the NEWEST output
	sidecarLogTrimEvery = 300     // seconds between size checks
)

var (
	sidecarLogMaxBytesStr  = strconv.Itoa(sidecarLogMaxBytes)
	sidecarLogKeepBytesStr = strconv.Itoa(sidecarLogKeepBytes)
	sidecarLogTrimEveryStr = strconv.Itoa(sidecarLogTrimEvery)
)

// sidecarLogTrimOnce returns the /bin/sh for ONE pass of the log cap: if the
// log is over sidecarLogMaxBytes, rewrite it in place with its newest
// sidecarLogKeepBytes.
//
// It is a standalone `if` rather than the loop's original `|| continue` so a
// test can run the real shipped shell against a real file — the previous
// assertions only checked that the script mentioned the constants, which stayed
// green when the cap was raised to a value that caps nothing.
//
// Rewriting the same inode (`cat …tail >log`) rather than renaming is the whole
// point: the sidecar holds this file open for its entire life, so a rotation
// would leave it writing into an unlinked inode — output unreadable, pages
// still charged to the cgroup, strictly worse than the leak being closed.
func sidecarLogTrimOnce(logPath string) string {
	return `if [ $(wc -c <` + logPath + ` 2>/dev/null || echo 0) -gt ` + sidecarLogMaxBytesStr + ` ]; then` + "\n" +
		`  tail -c ` + sidecarLogKeepBytesStr + ` ` + logPath + ` >` + logPath + `.tail 2>/dev/null && cat ` + logPath + `.tail >` + logPath + "\n" +
		`  rm -f ` + logPath + `.tail` + "\n" +
		`fi`
}

// sidecarLaunchScript builds the /bin/sh program that starts crewship-sidecar,
// bounds its log, and reports health through the exec's exit code.
//
// The log cap is a trim in place rather than a rotation. The sidecar holds the
// log open for its whole life, so renaming the file would leave it writing into
// an unlinked inode — the output becomes unreadable while its pages stay
// charged to the cgroup, i.e. strictly worse than the leak we are closing.
// Rewriting the same inode with its own tail keeps the writer's fd valid and
// keeps the most recent output, which is the half anyone debugging wants.
//
// Two details are load-bearing:
//
//   - the log is truncated (`: >`) before launch, so a sidecar restarted inside
//     a long-lived crew container does not inherit the previous one's bytes;
//   - stderr is opened with `2>>` (O_APPEND) rather than `2>`. An appending
//     writer reseeks to the current end on every write, so it picks up the
//     shorter file after a trim. A plain `2>` writer would keep its old offset
//     and refill the gap with a sparse hole, and the cap would never hold.
//
// The check is periodic, so the real bound is the cap plus whatever one
// interval of stderr adds — not a hard ceiling, but a bounded one, which is
// what the cgroup cares about.
//
// The trimmer exits with the sidecar (`kill -0`), so a crew that restarts its
// sidecar does not accumulate one orphaned loop per restart against PidsLimit.
func sidecarLaunchScript(credsB64 string) string {
	return fmt.Sprintf(
		// Start from an empty log.
		`: >%[2]s`+"\n"+
			// Pipe credentials in as base64 on stdin (never argv — see the
			// shell-injection note at the call site). stdout/stderr go to files
			// so the sidecar survives the docker exec stream closing; writes to
			// a closed pipe would SIGPIPE it.
			`echo '%[1]s' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 >/dev/null 2>>%[2]s &`+"\n"+
			`SIDECAR_PID=$!`+"\n"+
			// Background size cap. Detached from the exec's stdio so it neither
			// holds the stream open nor takes a SIGPIPE when it closes.
			`while sleep %[4]s; do`+"\n"+
			`  kill -0 $SIDECAR_PID 2>/dev/null || break`+"\n"+
			`%[3]s`+"\n"+
			`done </dev/null >/dev/null 2>&1 &`+"\n"+
			// Health check: verify the sidecar answers, exit 1 on failure so the
			// orchestrator hears about it.
			`sleep 0.5`+"\n"+
			`if wget -q -O /dev/null http://127.0.0.1:9119/health 2>/dev/null; then exit 0; `+
			`elif curl -sf http://127.0.0.1:9119/health >/dev/null 2>&1; then exit 0; `+
			`else echo "sidecar health check failed" >&2; exit 1; fi`,
		credsB64,
		sidecarLogPath,
		"  "+strings.ReplaceAll(sidecarLogTrimOnce(sidecarLogPath), "\n", "\n  "),
		sidecarLogTrimEveryStr,
	)
}

// sidecarIPCToken returns the internal-API token to hand a sidecar at
// startup. Since #1159 it is CREW-bound when the run carries a crew —
// HMAC(master, workspaceID‖crewID), format crwv1.<ws>.<crew>.<mac> — so
// the API middleware can pin the crew scope server-side instead of
// trusting a caller-supplied ?crew_id (the #1031/#1159 metadata-enumeration
// leak). A run without a crew (crewID == "") falls back to the
// workspace-bound token (PR-F24), keeping the crew-less workspace-wide
// behaviour the in-process TokenSyncer and crew-less callers rely on.
//
// Either way the raw master token never enters a container: any agent that
// captured a derived token there (UID escalation, memory dump) can only act
// within the workspace — and now the crew — baked into it. Derivation is
// stateless HMAC, validated against the same in-memory master for one boot.
//
// Fail closed: with an empty master or an empty workspace there is
// nothing safe to issue — return "" so the sidecar's IPC calls get
// loud 403s instead of a process-wide secret. An empty workspace here
// would indicate a bug upstream (IPC configs are only built for crew
// runs, which always carry a workspace).
func sidecarIPCToken(master, workspaceID, crewID string, logger *slog.Logger) string {
	if master == "" {
		return ""
	}
	if workspaceID == "" {
		logger.Error("sidecar IPC token: empty workspace_id — refusing to issue a token (master token never enters containers)")
		return ""
	}
	if crewID != "" {
		return internaltoken.DeriveCrewToken(master, workspaceID, crewID)
	}
	return internaltoken.DeriveWorkspaceToken(master, workspaceID)
}

// agentAuthToken returns the per-agent bearer token to inject into an agent's
// env + MCP config (#812). It extends sidecarIPCToken's workspace binding down
// to the individual agent — HMAC(master, workspaceID‖agentID) — so a shared
// per-crew sidecar can attribute a call to the ACTING agent instead of a
// caller-supplied `from`/slug that any sibling could spoof.
//
// Fail closed exactly like sidecarIPCToken: an empty master, workspace, or
// agent id yields "" (no token), so the sidecar's identity resolution refuses
// the call rather than falling back to a process-wide secret or a wildcard.
func agentAuthToken(master, workspaceID, agentID string, logger *slog.Logger) string {
	if master == "" {
		return ""
	}
	if workspaceID == "" || agentID == "" {
		logger.Error("per-agent token: empty workspace_id or agent_id — refusing to issue",
			"workspace_id", workspaceID, "agent_id", agentID)
		return ""
	}
	return internaltoken.DeriveAgentToken(master, workspaceID, agentID)
}

// PreRunInstallPackages installs system packages as root before the agent starts.
// The agent runs as UID 1001 (non-root) and cannot install apt packages itself.
// This function runs `apt-get install` as root (UID 0), then the agent exec
// runs as UID 1001 with the packages available in PATH.
func PreRunInstallPackages(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	packages []string,
	logger *slog.Logger,
) error {
	if len(packages) == 0 {
		return nil
	}

	// Sanitize package names: only allow alphanumeric, dash, dot, plus.
	// F9 (2026-06 audit): additionally reject empty tokens and any token
	// beginning with '-'. apt treats a leading-dash argument as a FLAG, not a
	// package — so "--reinstall" / "-y" would otherwise pass the per-char
	// check and be spliced straight into `apt-get install … <pkg>`, letting a
	// caller alter apt's behaviour. Internal dashes (e.g. "ca-certificates")
	// remain valid; only a leading dash is a flag.
	for _, pkg := range packages {
		if pkg == "" || pkg[0] == '-' {
			return fmt.Errorf("invalid package name: %q", pkg)
		}
		for _, c := range pkg {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '+') {
				return fmt.Errorf("invalid package name: %q", pkg)
			}
		}
	}

	script := "apt-get update -qq && apt-get install -y -qq " + strings.Join(packages, " ")
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "0:0",
		// Installing OS packages requires root; #1158 opt-in (see ExecConfig).
		AllowPrivileged: true,
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pre-run install: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Info("pre-run packages installed",
		"container_id", shortID(containerID),
		"packages", packages,
	)
	return nil
}

// credFileSpec is the per-file plan emitted by buildCredFileScript.
// One Credential may expand into multiple specs (USERPASS → 2 entries),
// or into one spec with a non-default mode (SSH_KEY → 0600 in ssh/
// subdir). Pulled out so tests can assert the expansion shape without
// going through a container exec.
type credFileSpec struct {
	EnvVar string // .env mapping key (e.g. GMAIL_USERNAME, GITHUB_SSH_PATH)
	Value  string // raw cleartext bytes to write to the file
	Path   string // absolute path inside the container, e.g. /secrets/agent/ssh/github
	Mode   string // octal string for chmod, e.g. "0400" or "0600"
}

// credNameSkip is one credential — or one part of one — that was NOT written
// because its name cannot be an environment variable (#1657).
//
// Carried out of buildCredFileScript rather than logged inside it, for the
// reason credential_field_delivery.go already records on its own conflicts:
// the function is a pure builder that tests call directly, and the caller is
// the one holding a logger and the agent's identity. It never carries a value.
type credNameSkip struct {
	CredentialID string
	EnvVar       string
	Type         string
	Reason       string
}

// buildCredFileScript translates a slice of decrypted credentials into
// the shell script that mounts them into the agent container and the
// list of .env entries the agent reads at startup.
//
// Which types are file-delivered is NOT decided by the switch below — it is
// read from the credpolicy table (Delivery=file), the single source of truth
// shared with secrets_cleanup.go. The switch only picks the on-disk LAYOUT for
// each file type; a Delivery=file type it doesn't recognise falls through to
// the default flat-file layout. Non-file types are skipped before the switch.
//
// Per-type behaviour:
//
//	API_KEY (proxy), AI_CLI_TOKEN / OAUTH2 / ENDPOINT_URL (env), unknown (none)
//	                               → skipped (not Delivery=file in credpolicy);
//	                                 the sidecar proxy / env-var block handle
//	                                 the delivered ones, so they never touch disk
//	CLI_TOKEN, GENERIC_SECRET      → one file at secretsAgentDir/<envvar>,
//	                                 mode 0400. .env maps envvar to path.
//	SECRET                         → same flat-file layout as CLI_TOKEN,
//	                                 BUT ONLY when Keeper is disabled. With
//	                                 Keeper enabled the agent's system prompt
//	                                 tells it "you do NOT have these
//	                                 credentials in your environment" and the
//	                                 env-delivery path withholds them
//	                                 (exec_env.go); writing the cleartext file
//	                                 anyway would contradict that prompt and
//	                                 defeat Keeper's access-control + audit
//	                                 gate. Under Keeper the agent must fetch
//	                                 the value via the Keeper API instead.
//	USERPASS                       → two files <envvar>_USERNAME and
//	                                 <envvar>_PASSWORD, mode 0400.
//	                                 Cleartext username is stored on the
//	                                 Credential struct, not encrypted (it's
//	                                 an identifier, not a secret —
//	                                 matches Bitwarden's login.username).
//	SSH_KEY                        → file at secretsAgentDir/ssh/<envvar>,
//	                                 mode 0600 (OpenSSH refuses world-
//	                                 readable keys; 0600 is the strictest
//	                                 mode the client still accepts).
//	                                 .env exposes <envvar>_PATH so the
//	                                 agent can locate the key without
//	                                 hardcoding the convention.
//	CERTIFICATE                    → file at secretsAgentDir/certs/<envvar>.pem,
//	                                 mode 0400. Same _PATH helper env var.
//
// Returns the joined-with-&& script ready for `sh -c`, the count of files
// mounted (for logging), and every credential or part that was DROPPED for an
// unusable name so the caller can report it. A skipped credential's name is
// never checked — see the ordering note in the loop (#1652). Empty input yields
// ("", 0, nil, nil) so callers can early-exit without a noop exec.
func buildCredFileScript(creds []Credential, secretsAgentDir string, keeperEnabled bool) (string, int, []credNameSkip, error) {
	var specs []credFileSpec
	var envLines []string
	var skipped []credNameSkip

	for _, c := range creds {
		if c.EnvVarName == "" || c.PlainValue == "" {
			continue
		}

		// #1364: Keeper-gated types are withheld from ALL delivery when Keeper
		// is on — the agent fetches them via /keeper/request. Applied here
		// generically (credpolicy table) rather than as a SECRET-only special
		// case, so a newly-gated type is covered without editing this switch.
		// Defense-in-depth: the resolver already blanks these values, so in the
		// normal flow PlainValue is "" and the empty-value guard above already
		// skipped them; this is the belt to that resolver-side braces.
		if keeperEnabled && credpolicy.IsKeeperGated(c.Type) {
			continue
		}

		// #1364: the DELIVERY CHANNEL is table-driven too. Only file-mounted
		// types (credpolicy Delivery=file) are written under /secrets; env /
		// proxy / none types are injected elsewhere (env-var block, sidecar
		// proxy) or not delivered at all, so they're skipped here. Consulting
		// the table — rather than a hard-coded type list in the switch below —
		// keeps this delivery decision in lockstep with secrets_cleanup.go,
		// which counts mounted files via credpolicy.For(...).FileMounted(). A
		// future Delivery=file type added to the table is now written (via the
		// default flat-file layout) and counted, instead of being counted by
		// cleanup yet never written here.
		if !credpolicy.For(c.Type).FileMounted() {
			continue
		}

		// The name check runs AFTER both skips, and the order is the fix for
		// #1652 rather than a tidiness preference. A name is validated here
		// because it becomes a path component and is interpolated into `sh -c`
		// — a credential that writes no file has neither, so holding it to the
		// env-var charset is a rule with no subject.
		//
		// It had one anyway, and the cost was the whole batch. The API mints a
		// synthetic `_OAUTH_ACCESS_TOKEN:<credID>` entry for every OAuth MCP
		// binding (agent_config.go resolveOAuthAccessTokens) so
		// injectMCPOAuthTokens can write the server's tokens.json; the colon
		// and the uuid fail the charset, and validating first turned an OAUTH2
		// credential this function was about to skip into a hard error that
		// abandoned every OTHER credential in the slice.
		//
		// #1657: skipping rather than erroring is now the answer for a
		// file-delivered credential too, and for the same reason one level up.
		// Reordering fixed the synthetic OAuth entry; it did nothing for a
		// `CLI_TOKEN` a user had named `github-token` in the UI, which IS
		// file-delivered and so reached this line for real. A hard error here
		// is fatal at the caller — preparePreflightDirs treats a failed
		// credential write on a fileCreds run as a dead run — so one badly
		// named credential cost the agent every other credential AND the run,
		// with an error that also advised upgrading Docker Engine because that
		// advice is appended to the same string.
		//
		// This function does not RENAME. The API tier normalises names against
		// a claim table covering the agent's whole delivery set
		// (internal/api/credential_delivery.go); a rename invented here would
		// have no such table and could quietly point two credentials at one
		// file. Everything that arrives already named is either writable or
		// dropped, and a drop is reported by the caller.
		if !credname.Valid(c.EnvVarName) {
			skipped = append(skipped, credNameSkip{
				CredentialID: c.ID, EnvVar: c.EnvVarName, Type: c.Type,
				Reason: "not a valid environment variable name, so it cannot become a file under /secrets",
			})
			continue
		}

		switch c.Type {
		case "USERPASS":
			// Username is cleartext on the Credential struct; password
			// rides on PlainValue (encrypted at rest, decrypted by the
			// resolver). Both must be present — the validator at the
			// API tier enforces that, so empty username here means a
			// data-shape regression we'd rather surface than silently
			// inject "" as the username.
			if c.Username == "" {
				return "", 0, skipped, fmt.Errorf("USERPASS credential %q missing username", c.EnvVarName)
			}
			userPath := secretsAgentDir + "/" + c.EnvVarName + "_USERNAME"
			passPath := secretsAgentDir + "/" + c.EnvVarName + "_PASSWORD"
			specs = append(specs,
				credFileSpec{EnvVar: c.EnvVarName + "_USERNAME", Value: c.Username, Path: userPath, Mode: "0400"},
				credFileSpec{EnvVar: c.EnvVarName + "_PASSWORD", Value: c.PlainValue, Path: passPath, Mode: "0400"},
			)
			envLines = append(envLines,
				c.EnvVarName+"_USERNAME="+userPath,
				c.EnvVarName+"_PASSWORD="+passPath,
			)

		case "SSH_KEY":
			// 0600 (not 0400) because some SSH client builds tolerate
			// 0400 but the canonical "strict" mode for id_rsa et al.
			// is 0600 — keeping it consistent with what ssh-keygen
			// writes by default avoids "WARNING: UNPROTECTED PRIVATE
			// KEY FILE" surprises when the agent runs ssh interactively.
			path := secretsAgentDir + "/ssh/" + c.EnvVarName
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0600",
			})
			envLines = append(envLines, c.EnvVarName+"_PATH="+path)

		case "CERTIFICATE":
			// Certs aren't keys — 0400 read-only is fine and stricter
			// than 0600 (no write bit). Helper env var name mirrors
			// SSH_KEY for consistency.
			path := secretsAgentDir + "/certs/" + c.EnvVarName + ".pem"
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0400",
			})
			envLines = append(envLines, c.EnvVarName+"_PATH="+path)

		default:
			// SECRET, CLI_TOKEN, GENERIC_SECRET — and any future Delivery=file
			// type without a bespoke layout — deliver as a single 0400 file (+
			// an env var pointing at the path). SECRET is the Keeper-gated one,
			// already skipped above when Keeper is on; CLI tools (gh, glab, …)
			// and generic secrets are read from disk regardless of Keeper state.
			// Non-file types never reach here: the FileMounted() guard above
			// skipped them (API_KEY → proxy, AI_CLI_TOKEN/OAUTH2/ENDPOINT_URL →
			// env var, unknown → not delivered).
			path := secretsAgentDir + "/" + c.EnvVarName
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0400",
			})
			envLines = append(envLines, c.EnvVarName+"="+path)
		}

		// Multi-part credentials (PRD-CREDENTIALS-V2 §2.2). USERPASS already
		// established that one credential can become several delivered files;
		// this is that idea without a bespoke type — one flat 0400 file per
		// part, named by the API tier, .env mapping name → path like every
		// other file here.
		//
		// Flat regardless of the credential's type, on purpose. The per-type
		// layouts above encode what the PRIMARY value is: an SSH key needs 0600
		// under ssh/ because OpenSSH refuses anything looser, a certificate
		// wants a .pem the tooling can find. A passphrase or an account id is
		// neither of those, and giving it the key's layout would put a
		// non-key in ssh/ where the next reader assumes everything is a key.
		//
		// Reached only for a credential that is itself file-delivered and not
		// withheld: the Keeper gate and the credpolicy FileMounted() check are
		// both above, and both `continue`, so a part cannot be written for a
		// credential whose own value was not.
		for _, f := range c.Fields {
			if f.EnvVar == "" || f.Value == "" {
				continue
			}
			if !credname.Valid(f.EnvVar) {
				// Same treatment as the primary name, and the reasoning is the
				// same one level down: the API tier already drops any part it
				// could not name legally, so a bad name arriving here means an
				// unsanitised value reached the delivery path — and this script
				// is about to be interpolated into `sh -c`. The part is dropped
				// before it can be, and the credential it belongs to still
				// lands rather than taking the batch with it.
				skipped = append(skipped, credNameSkip{
					CredentialID: c.ID, EnvVar: f.EnvVar, Type: c.Type,
					Reason: "credential field name is not a valid environment variable name",
				})
				continue
			}
			fieldPath := secretsAgentDir + "/" + f.EnvVar
			specs = append(specs, credFileSpec{
				EnvVar: f.EnvVar, Value: f.Value, Path: fieldPath, Mode: "0400",
			})
			envLines = append(envLines, f.EnvVar+"="+fieldPath)
		}
	}

	if len(specs) == 0 {
		return "", 0, skipped, nil
	}

	// Pre-create the ssh/ and certs/ subdirectories with restrictive
	// perms before any file write. The script is exec'd as UID 1001
	// (matching the secretsAgentDir owner that orchestrator_run.go
	// mkdir'd before us), so file ownership lands on 1001:1001
	// automatically and we don't need chown. Earlier the script ran
	// as root and chown'd everything — that path fails silently in
	// production containers, which run with CapDrop:ALL and so
	// lack CAP_CHOWN + CAP_DAC_OVERRIDE; root inside such a container
	// can't write to a 1001-owned dir nor change ownership at all.
	//
	// TOCTOU defence on warm container restart: an agent process from
	// the previous session and the credential writer here both run as
	// UID 1001, which means the agent can plant a symlink inside
	// /secrets/<slug>/ pointing at any other 1001-writable path
	// (/crew/shared/.memory/..., /output/<other-agent>/...) and then
	// `echo … > path` follows that symlink, corrupting the linked
	// target with credential cleartext or, more usefully to the
	// attacker, with an empty .env that disables the next agent's
	// credential map. Each write therefore opens with `rm -f path`
	// first — the unlink removes the planted symlink (UID 1001 owns
	// the parent dir, so unlink succeeds regardless of the symlink's
	// target), and the subsequent shell redirect creates a fresh
	// regular file at the intended path. The two-step pattern is
	// safer than relying on `set -o noclobber` because that flag
	// makes the script fail on legitimate re-runs (re-apply of a
	// rotated credential), while rm-then-write is idempotent.
	scriptParts := []string{
		fmt.Sprintf("mkdir -p %s/ssh %s/certs", secretsAgentDir, secretsAgentDir),
		fmt.Sprintf("chmod 0700 %s/ssh %s/certs", secretsAgentDir, secretsAgentDir),
	}

	for _, s := range specs {
		// base64 round-trip prevents any shell interpretation of the
		// secret value — newlines in PEM bodies, single-quotes in
		// passwords, etc. all pass through opaquely. The leading
		// `rm -f` neutralises any pre-planted symlink (see TOCTOU
		// note on the script-parts block above) before the redirect
		// follows-or-creates.
		valB64 := base64.StdEncoding.EncodeToString([]byte(s.Value))
		scriptParts = append(scriptParts,
			fmt.Sprintf("rm -f %s", s.Path),
			fmt.Sprintf("echo '%s' | base64 -d > %s", valB64, s.Path),
			fmt.Sprintf("chmod %s %s", s.Mode, s.Path),
		)
	}

	// .env maps each env var to its file path (never the raw value), so
	// nothing sensitive ends up in /proc/<pid>/environ if the agent
	// spawns subprocesses that inherit the env block. Same `rm -f`
	// guard as the per-spec writes above.
	envContent := strings.Join(envLines, "\n") + "\n"
	envB64 := base64.StdEncoding.EncodeToString([]byte(envContent))
	envPath := secretsAgentDir + "/.env"
	scriptParts = append(scriptParts,
		fmt.Sprintf("rm -f %s", envPath),
		fmt.Sprintf("echo '%s' | base64 -d > %s", envB64, envPath),
		fmt.Sprintf("chmod 0400 %s", envPath),
		// Lock down the parent dir to 0700 so a future per-agent UID
		// layout can't list its sibling's contents. On the current
		// shared-UID setup (all agents run as 1001) this is a noop
		// but mirrors the principle-of-least-privilege intent the
		// pre-fix chown was trying to encode. The shared-UID posture
		// is deliberate and documented: the enforced isolation
		// boundary is the crew container, not the agent — agents that
		// must not read each other's secrets belong in separate crews
		// (docs/guides/credentials.mdx "The trust boundary is the
		// crew, not the agent"; #1086).
		fmt.Sprintf("chmod 0700 %s", secretsAgentDir),
	)

	return strings.Join(scriptParts, " && "), len(specs), skipped, nil
}

// writeCredentialFiles writes file-mountable credentials into the
// agent's secrets directory. Thin wrapper around buildCredFileScript
// that runs the resulting script as UID 1001 — the same UID that
// owns secretsAgentDir from the orchestrator_run.go mkdir pass.
//
// Earlier the script ran as UID 0 with chown lines, on the assumption
// that "root can do anything". That assumption is false inside Crewship's
// runtime containers: they're launched with CapDrop:["ALL"] plus
// ReadonlyRootfs and no-new-privileges, so root-without-capabilities
// can neither write to a 1001-owned dir (no CAP_DAC_OVERRIDE) nor
// chown any file (no CAP_CHOWN). The exec succeeded at the docker API
// level (returned no Go error), `io.Copy` drained an empty stdout, and
// we'd log "credential files written" while /secrets/<agent>/ stayed
// empty. Symptom in the wild: SPEC-4 sugar credentials showed up in
// agent_credentials but never reached the agent runtime, so any
// downstream code reading /secrets/<agent>/.env (or the matching
// per-credential file) saw nothing.
//
// Two changes close the gap:
//
//  1. Run as UID 1001 so the writes land via the owner-permission path
//     (no capability gymnastics needed). buildCredFileScript no longer
//     emits chown lines, which were the only ops requiring root.
//  2. After Exec returns, call ExecInspect and surface non-zero exit
//     codes as errors. Previously the orchestrator silently treated
//     "exec attached" as "exec succeeded" — the new check makes a
//     real failure (permission, disk full, sh parse error) bubble
//     up to the caller's warn-and-continue path instead of writing
//     a false-success log entry.
//
// Per-type behaviour is documented on buildCredFileScript, including the
// keeperEnabled gate that withholds SECRET file delivery when Keeper is on
// (the value must then be fetched via the Keeper API, matching the system
// prompt). The secretsSharedDir parameter is unused today but retained on
// the signature for the crew-shared credentials work tracked separately.
func writeCredentialFiles(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	agentSlug string,
	creds []Credential,
	secretsAgentDir string,
	secretsSharedDir string,
	keeperEnabled bool,
	logger *slog.Logger,
) error {
	script, fileCount, skipped, err := buildCredFileScript(creds, secretsAgentDir, keeperEnabled)
	// The skips are reported before the error check on purpose: a name that
	// could not be written is worth saying even on a run that then fails for an
	// unrelated reason, and it is the only trace an operator gets. A credential
	// that vanishes silently is the quieter half of the defect #1657 fixed —
	// the run survives, and the agent finds no GITHUB_TOKEN with nothing
	// anywhere connecting that to the name someone typed in the UI.
	for _, s := range skipped {
		logger.Warn("credential not delivered — its name cannot be an environment variable",
			"agent_slug", agentSlug, "credential_id", s.CredentialID,
			"env_var", s.EnvVar, "credential_type", s.Type, "reason", s.Reason)
	}
	if err != nil {
		return fmt.Errorf("build credential script: %w", err)
	}
	if script == "" {
		return nil
	}

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}

	// #1646: when a merged preflight script is active this step joins it,
	// which is how the base64-encoded credential material finally stops
	// riding argv (/proc/<pid>/cmdline is mode 0444 and a bare ps prints it —
	// the same exposure #1629 closed for the agent's bearer token). The
	// ExecInspect exit-code check below is not lost in that case: the merged
	// script reports each step's status by name and the caller checks
	// preflightStepCredentials against the flush result.
	if b, ok := ctr.(*preflightBatch); ok && b.accepts(cfg) {
		b.add(preflightStepCredentials, "", script)
		logger.Info("credential files queued for the merged preflight script",
			"agent_slug", agentSlug, "secrets_dir", secretsAgentDir, "file_count", fileCount)
		return nil
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write credential files: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	// Reading the stream to EOF means docker has closed the exec
	// pipe, which in turn means the process has exited and
	// ExecInspect will report the final exit code without racing.
	running, exitCode, inspectErr := ctr.ExecInspect(ctx, result.ExecID)
	if inspectErr != nil {
		return fmt.Errorf("inspect credential-file exec: %w", inspectErr)
	}
	if running {
		return fmt.Errorf("credential-file exec %s reported still running after EOF", result.ExecID)
	}
	if exitCode != 0 {
		return fmt.Errorf("credential-file script exited %d (agent_slug=%s, container=%s)",
			exitCode, agentSlug, containerID)
	}

	logger.Info("credential files written",
		"agent_slug", agentSlug,
		"secrets_dir", secretsAgentDir,
		"file_count", fileCount,
	)
	return nil
}

// sidecarHealth holds the parsed health response from a running sidecar.
type sidecarHealth struct {
	Status      string `json:"status"`
	NetworkMode string `json:"network_mode"`
	// SidecarHash is the content hash of the binary the running sidecar is
	// executing (#1008). Empty on pre-#1008 sidecars.
	SidecarHash string `json:"sidecar_hash"`
	// DomainsHash is a content hash of the running sidecar's CURRENT domain
	// allowlist (#1160). Empty on pre-#1160 sidecars. Compared against the
	// desired policy's hash so a restricted-mode crew only restarts its
	// sidecar when the allowlist actually changed, instead of on every exec.
	DomainsHash string `json:"domains_hash"`
	// Stale is set by checkSidecar when SidecarHash differs from the hash of
	// the sidecar binary currently on disk — i.e. the container is serving an
	// OLD bind-mounted sidecar after a redeploy. Not part of the wire format.
	Stale bool `json:"-"`
	// TokenFP (#1385) is the sidecar's fingerprint of the crew-bound internal
	// token it booted with (internaltoken.Fingerprint). Empty on a pre-#1385
	// sidecar or a crew-less one. The reap-orphan-containers path compares it
	// against the fingerprint of the token the server would mint today: a
	// non-empty mismatch means this container was orphaned by a master rotation
	// and now holds a permanently-rejected token.
	TokenFP string `json:"token_fp"`
	// ConfigFingerprint identifies the exact credential set the running
	// sidecar received. It is keyed, not a plain secret hash, so the
	// loopback health endpoint cannot become an offline credential oracle.
	ConfigFingerprint string `json:"config_fingerprint"`
}

// sidecarHashReporter is the optional capability a ContainerProvider implements
// to report the content hash of the crewship-sidecar binary it bind-mounts
// (the docker provider hashes cfg.SidecarBinaryPath). checkSidecar type-asserts
// for it to detect a stale running sidecar (#1008); providers that can't report
// return "" and detection fails open.
type sidecarHashReporter interface {
	ExpectedSidecarHash() string
}

// checkSidecar checks if a sidecar proxy is already listening on port 9119
// inside the given container. Returns nil if not running. If running, returns
// its current health state including network_mode.
func checkSidecar(ctx context.Context, ctr provider.ContainerProvider, containerID string) *sidecarHealth {
	if ctr == nil {
		return nil
	}
	result, err := ctr.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "curl -sf http://127.0.0.1:9119/health 2>/dev/null || wget -q -O - http://127.0.0.1:9119/health 2>/dev/null"},
		User:        "1002:1002",
	})
	if err != nil {
		return nil
	}
	output, _ := io.ReadAll(result.Reader)
	result.Reader.Close()
	var h sidecarHealth
	if err := json.Unmarshal(output, &h); err != nil {
		return nil
	}
	if h.Status != "ok" {
		return nil
	}
	// #1008: flag a container serving a STALE bind-mounted sidecar. A bind
	// mount pins the inode the container started with, so after a redeploy the
	// running sidecar keeps executing the OLD binary (memory/egress can degrade
	// silently) while the host binary is already the NEW one. Compare the hash
	// the sidecar reports against the hash of the binary now on disk. Fail open:
	// only flag when BOTH hashes are known and differ, so a pre-#1008 sidecar
	// (empty hash) or a provider that can't report never trips a false alarm.
	if reporter, ok := ctr.(sidecarHashReporter); ok {
		if expected := reporter.ExpectedSidecarHash(); expected != "" && h.SidecarHash != "" && expected != h.SidecarHash {
			h.Stale = true
		}
	}
	return &h
}

// SidecarTokenFP probes a running container's sidecar /health and returns the
// crew-bound internal-token fingerprint it advertises (#1385). Returns "" when
// the sidecar isn't reachable/healthy, is crew-less, or predates the token_fp
// field — every one of which the caller treats as "unknown", NEVER as
// orphaned. Thin exported wrapper over checkSidecar so the admin
// reap-orphan-containers handler (internal/api) can classify a container
// without reimplementing the /health exec probe.
func SidecarTokenFP(ctx context.Context, ctr provider.ContainerProvider, containerID string) string {
	h := checkSidecar(ctx, ctr, containerID)
	if h == nil {
		return ""
	}
	return h.TokenFP
}

// SidecarTokenOrphaned reports whether a container's sidecar holds a STALE
// crew-bound token: the fingerprint it advertises on /health (reportedFP)
// disagrees with the fingerprint of the token the server would mint for that
// crew today (expectedFP). It is the #1385 orphan test.
//
// Fails SAFE: an empty reportedFP (unreachable / pre-#1385 / crew-less
// sidecar) or an empty expectedFP (internal auth unconfigured) is NEVER
// classified as orphaned, so a reap only ever removes a container we can
// positively prove is holding a rotated-master token — a healthy container is
// never touched on ambiguity.
func SidecarTokenOrphaned(reportedFP, expectedFP string) bool {
	if reportedFP == "" || expectedFP == "" {
		return false
	}
	return reportedFP != expectedFP
}

// DomainsHash mirrors internal/sidecar.DomainAllowlist.Hash() byte-for-byte
// (lower-case + dedupe via a set, sort, sha256, hex[:12]) — reimplemented
// here rather than shared because internal/sidecar imports this package
// (mission.go), so the reverse import would cycle. Any change to one side
// MUST be mirrored on the other or #1160's restart-skip silently stops
// matching and every restricted-mode exec restarts the sidecar again.
//
// Exported (#1232) so internal/sidecar's parity test
// (TestHealthDomainsHashParity_* in domains_hash_parity_test.go) can assert
// the lockstep across the real wire format: the hash a freshly configured
// sidecar reports on /health MUST equal this function over the exact
// desiredDomains RunAgent computes, or every exec into a restricted-mode
// crew silently degrades back to a guaranteed sidecar restart.
func DomainsHash(domains []string) string {
	set := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		set[strings.ToLower(d)] = struct{}{}
	}
	sorted := make([]string, 0, len(set))
	for d := range set {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	h := sha256.New()
	for _, d := range sorted {
		h.Write([]byte(d))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// sidecarNeedsRestart reports whether a shared crew container's already-
// running sidecar must be killed and relaunched to match the desired
// network policy (#1160). True on an actual network-mode change. In
// restricted mode it's true only when the allowlist itself changed since
// the sidecar started — NOT unconditionally on every exec, which used to
// turn every other agent's exec in the same shared container into a
// guaranteed restart of an otherwise-healthy sidecar (the repeated
// non-deploy-triggered "stale" false positives traced live on dev3 are
// that restart cycle racing itself). An unknown running hash (a
// pre-#1160 sidecar that doesn't report domains_hash) can't prove the
// allowlist is unchanged, so it fails toward the old, safe-but-noisier
// behaviour rather than risk skipping a genuinely-needed restart.
func sidecarNeedsRestart(health *sidecarHealth, desiredMode string, desiredDomains []string, desiredConfigFingerprint string) bool {
	if health.NetworkMode != desiredMode {
		return true
	}
	if desiredConfigFingerprint != "" && health.ConfigFingerprint != desiredConfigFingerprint {
		return true
	}
	if desiredMode != "restricted" {
		return false
	}
	if health.DomainsHash == "" {
		return true
	}
	return health.DomainsHash != DomainsHash(desiredDomains)
}

// startSidecar launches the crewship-sidecar proxy inside the container.
// It pipes credentials via stdin JSON and waits for the "SIDECAR_READY" signal.
// The sidecar runs as a background process and intercepts all agent HTTP traffic.
// SidecarMemoryConfig is passed to the sidecar binary via stdin when memory is enabled.
type SidecarMemoryConfig struct {
	Enabled        bool   `json:"enabled"`
	BasePath       string `json:"base_path"`
	AgentSlug      string `json:"agent_slug"`
	AgentRole      string `json:"agent_role"`       // "lead" or "agent"
	CrewMemoryPath string `json:"crew_memory_path"` // e.g. /crew/shared/.memory
}

// SidecarIPCConfig provides the crewshipd internal API address for the sidecar,
// allowing lead agents to forward assignment requests back to crewshipd.
// ContainerID is the Docker container ID where this agent is running; the sidecar
// forwards it to crewshipd so /keeper/execute can run commands in the right container.
type SidecarIPCConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	AgentID     string `json:"agent_id"`
	AgentSlug   string `json:"agent_slug"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
	ContainerID string `json:"container_id"`
	// AgentToken is the boot agent's per-agent bearer token (#812). The sidecar
	// matches an inbound Authorization: Bearer token against this + each crew
	// member's AuthToken to resolve the ACTING agent's identity.
	AgentToken string `json:"agent_token,omitempty"`
}

// SidecarRouteAuth lets the sidecar validate any agent's derived LLM route
// token without receiving the process-wide master or a complete crew roster.
// Key is independently scoped to this workspace/crew and cannot authorize
// internal API calls.
type SidecarRouteAuth struct {
	Key string `json:"key"`
}

// SidecarCrewMember describes a crew member accessible to lead agents for assignment.
type SidecarCrewMember struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RoleTitle string `json:"role_title"`
	ChatID    string `json:"chat_id,omitempty"`
	// AuthToken is this member's per-agent bearer token (#812) — see
	// SidecarIPCConfig.AgentToken.
	AuthToken string `json:"auth_token,omitempty"`
}

// SidecarNetworkPolicy configures crew-level network access for the sidecar.
type SidecarNetworkPolicy struct {
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// AllowPrivateEndpoints (#961) lets the sidecar's dial-time SSRF guard
	// permit RFC1918/loopback destinations (a crew-opted-in on-prem/LAN model
	// endpoint). Link-local/metadata stay blocked regardless.
	AllowPrivateEndpoints bool `json:"allow_private_endpoints,omitempty"`
}

func startSidecar(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	creds []Credential,
	memoryCfg *SidecarMemoryConfig,
	ipcCfg *SidecarIPCConfig,
	routeAuth *SidecarRouteAuth,
	crewMembers []SidecarCrewMember,
	networkPolicy *SidecarNetworkPolicy,
	mcpServers []MCPServerConfig,
	configFingerprint string,
	logger *slog.Logger,
) error {
	sc := buildSidecarCreds(creds, logger)

	// Build the input payload (new object format that includes memory config and IPC config)
	type sidecarMCPServer struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		DisplayName string            `json:"display_name"`
		Transport   string            `json:"transport"`
		Endpoint    string            `json:"endpoint,omitempty"`
		Command     string            `json:"command,omitempty"`
		Args        []string          `json:"args,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
		Credential  *MCPCredential    `json:"credential,omitempty"`
	}
	type sidecarInput struct {
		Credentials       []sidecarCred         `json:"credentials"`
		Memory            *SidecarMemoryConfig  `json:"memory,omitempty"`
		IPC               *SidecarIPCConfig     `json:"ipc,omitempty"`
		RouteAuth         *SidecarRouteAuth     `json:"route_auth,omitempty"`
		CrewMembers       []SidecarCrewMember   `json:"crew_members,omitempty"`
		NetworkPolicy     *SidecarNetworkPolicy `json:"network_policy,omitempty"`
		MCPServers        []sidecarMCPServer    `json:"mcp_servers,omitempty"`
		ConfigFingerprint string                `json:"config_fingerprint,omitempty"`
	}

	// Only pass HTTP servers to sidecar — stdio servers are handled
	// by Claude Code directly via .mcp.json, not the gateway.
	var mcpInput []sidecarMCPServer
	for _, s := range mcpServers {
		if s.Transport != "streamable-http" {
			continue
		}
		// sidecarMCPServer has identical fields & JSON tags to
		// MCPServerConfig; the anonymous type exists only so the JSON
		// envelope stays scoped to this function. A direct conversion
		// keeps the two in lockstep — field-by-field copy would silently
		// drift if orchestrator.MCPServerConfig gains a field.
		mcpInput = append(mcpInput, sidecarMCPServer(s))
	}

	input := sidecarInput{
		Credentials:       sc,
		Memory:            memoryCfg,
		IPC:               ipcCfg,
		RouteAuth:         routeAuth,
		CrewMembers:       crewMembers,
		NetworkPolicy:     networkPolicy,
		MCPServers:        mcpInput,
		ConfigFingerprint: configFingerprint,
	}

	credsJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal sidecar input: %w", err)
	}

	// Prepare memory directories with shared-write perms BEFORE launching
	// the sidecar (UID 1002). The agent home `/crew/agents/{slug}` and the
	// crew share `/crew/shared` are bind-mounted at chown 1001:1001 mode
	// 0755 by the docker init container — the sidecar can't MkdirAll into
	// either path because group/other lack write, and any pre-existing
	// `.memory` subdir inherits the same restrictive perms.
	//
	// Run a one-shot root exec that:
	//   * pre-creates the per-agent and crew-shared `.memory` directories
	//   * chowns them to user=1001 (agent) group=1002 (sidecar)
	//   * applies setgid + g+rwx so new files/dirs inherit group 1002 with
	//     the container entrypoint's umask 0002 making them g+rw
	//
	// Both UIDs can then read+write the FTS5 SQLite index and plaintext
	// markdown tier files (#530). Best-effort: failures are logged but
	// don't block sidecar startup — without these perms the path-validator
	// fallback path still works for boot-context recall.
	if memoryCfg != nil && memoryCfg.Enabled && memoryCfg.BasePath != "" {
		// Prep EVERY crew member's memory dir, not just the requesting
		// agent's: members share this sidecar, and the per-agent MCP
		// path (/mcp/memory/<slug>, CRE-137) lets any of them write
		// their own tier — which needs the same 1001:1002 g+rwXs perms
		// the first agent gets.
		paths := memoryPrepPaths(memoryCfg, crewMembers)
		prepMemoryDirs(ctx, container, containerID, paths, logger)
	}

	// SECURITY: Base64-encode the credentials JSON to prevent shell injection.
	// Raw JSON piped through `echo '...'` is vulnerable to shell metacharacter
	// injection if a credential token contains single quotes or other shell chars.
	credsB64 := base64.StdEncoding.EncodeToString(credsJSON)

	script := sidecarLaunchScript(credsB64)

	// SECURITY: Run sidecar as UID 1002 (not 1001) so the agent process
	// cannot read /proc/<sidecar_pid>/mem to extract credentials from heap.
	// Linux kernel restricts /proc/PID/mem access to same-UID processes.
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1002:1002",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}

	output, readErr := io.ReadAll(result.Reader)
	result.Reader.Close()

	// Check if the health check script exited with an error
	running, exitCode, inspErr := container.ExecInspect(ctx, result.ExecID)
	if inspErr != nil {
		return fmt.Errorf("inspect sidecar exec: %w", inspErr)
	}
	if !running && exitCode != 0 {
		msg := strings.TrimSpace(string(output))
		if readErr != nil {
			msg += fmt.Sprintf(" (read error: %v)", readErr)
		}
		return fmt.Errorf("sidecar health check failed (exit %d): %s", exitCode, msg)
	}

	logger.Info("sidecar started",
		"container_id", shortID(containerID),
		"credentials", len(sc),
		"output_bytes", len(output),
	)
	return nil
}

// sidecarCred is one credential as the sidecar's CredStore receives it. Field
// names and JSON tags must match sidecar.Credential — the sidecar unmarshals the
// boot payload straight into that type, so a tag that drifts here does not fail
// to compile, it silently delivers a credential with a missing field.
type sidecarCred struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Token    string `json:"token"`
	Priority int    `json:"priority"`
	// LeaseExpiresAt hands the grant's #1373 lease deadline to the CredStore
	// so a leased provider key stops being served when its TTL lapses,
	// instead of living as long as the container.
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	// BaseURL and Headers are the credential-supplied upstream for a provider
	// whose endpoint is not a constant (OPENAI_COMPAT). omitempty keeps the
	// payload byte-identical for every credential without them, so an older
	// sidecar sees exactly the bytes it saw before these fields existed.
	BaseURL string            `json:"base_url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// sidecarConfigFingerprint returns a secret-safe identity for the exact
// credential configuration a run needs. The master internal token is the
// HMAC key and never enters the container; /health exposes only the result, so
// even a low-entropy custom-header value cannot be guessed offline. Agent
// identity is deliberately separate: a route token is self-verifying under the
// crew-bound route key, so agents with the same credentials can reuse safely.
//
// Empty key returns no fingerprint. Internal auth is required in production;
// a test/dev deployment that explicitly omits it must not publish an unkeyed
// digest of credentials merely to gain a restart optimisation. Such a legacy
// deployment retains its pre-existing reuse behaviour; production config
// always supplies the key.
func sidecarConfigFingerprint(key string, creds []Credential) string {
	if key == "" {
		return ""
	}
	sc := buildSidecarCreds(creds, nil)
	sort.SliceStable(sc, func(i, j int) bool {
		if sc[i].ID != sc[j].ID {
			return sc[i].ID < sc[j].ID
		}
		if sc[i].Provider != sc[j].Provider {
			return sc[i].Provider < sc[j].Provider
		}
		return sc[i].Priority < sc[j].Priority
	})
	payload, err := json.Marshal(struct {
		Credentials []sidecarCred `json:"credentials"`
	}{Credentials: sc})
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("crewship-sidecar-config-v1\x00"))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))[:24]
}

// credentialIsolationFailedOpen reports whether this run hands the sidecar a
// proxy-servable provider credential that it cannot bind to a configuration
// identity.
//
// sidecarConfigFingerprint returns "" when no internal token is configured, and
// an empty fingerprint is what makes Proxy.authorizeLLMRoute skip the route-token
// check entirely — every agent sharing the crew container could then reach
// whichever credential the proxy currently serves.
//
// Defence in depth, not an operational state: config.Load cannot produce an
// empty internal token (operator value, else derived from ENCRYPTION_KEY, else a
// per-boot random), so a crewshipd-launched sidecar always carries a fingerprint.
// This guards the invariant against a future path that reaches the orchestrator
// without going through Load.
//
// The gate asks whether the credential is servable by the LLM proxy, not merely
// whether the CredStore loads it. Those differ: CURSOR and FACTORY deliberately
// have no llmroute spec (see credTypeToProvider), so buildSidecarCreds keeps them
// for their CredStore counts while the proxy's only two Select calls — both
// spec-keyed — can never hand them out. Gating on CredStore membership would
// raise the isolation alarm for a run whose sole credential is CURSOR_API_KEY,
// and an alarm that names an exposure nobody can reach is one operators learn to
// skip past. The fingerprint test short-circuits, so this scan only runs on the
// already-degraded path.
func credentialIsolationFailedOpen(configFingerprint string, creds []Credential) bool {
	if configFingerprint != "" {
		return false
	}
	for _, sc := range buildSidecarCreds(creds, nil) {
		// Lookup, not LookupProvider: sc.Provider is the canonical spec ID
		// credTypeToProvider already resolved, not the free-text column.
		if _, ok := llmroute.Lookup(sc.Provider); ok {
			return true
		}
	}
	return false
}

// buildSidecarCreds maps the delivered credentials onto the sidecar boot
// payload, dropping every credential the CredStore has no provider for.
//
// Package-level rather than inline in startSidecar so the payload the sidecar
// actually receives can be asserted directly, instead of through a container
// exec script.
func buildSidecarCreds(creds []Credential, logger *slog.Logger) []sidecarCred {
	sc := []sidecarCred{}
	for _, c := range creds {
		prov := credTypeToProvider(c)
		if prov == "" {
			// A credential the vault delivered but the CredStore will never
			// hold. Most are correct drops (OAuth tokens, GitHub PATs, an
			// agent's own SECRET), so this is not a warning — but before it was
			// logged at all, a genuinely misrouted LLM key vanished here with
			// no trace and surfaced only as a 401 from the upstream, which
			// blames the key rather than the delivery.
			if logger != nil {
				logger.Debug("credential not routed to sidecar credstore",
					"credential_id", c.ID, "env_var", c.EnvVarName,
					"type", c.Type, "provider", c.Provider)
			}
			continue
		}
		sc = append(sc, sidecarCred{
			ID:             c.ID,
			Provider:       prov,
			Token:          c.PlainValue,
			Priority:       c.Priority,
			LeaseExpiresAt: c.LeaseExpiresAt,
			BaseURL:        c.BaseURL,
			Headers:        c.Headers,
		})
	}
	return sc
}

// credTypeToProvider maps orchestrator credential types to sidecar provider types.
// AI_CLI_TOKEN (OAuth) returns "" — these are injected directly as CLAUDE_CODE_OAUTH_TOKEN
// env var in BuildEnvVarsSidecar rather than stored in the sidecar CredStore, because
// the sidecar CredStore only supports x-api-key injection which won't work for OAuth tokens.
//
// The env-var switch runs FIRST and unchanged, so every credential that reaches
// the CredStore today reaches it identically. The provider column is consulted
// ONLY when the switch returns "" — i.e. only in the case that silently dropped
// the credential before. Strictly additive.
//
// The ordering is not an implementation detail. Preferring the column would
// change where an existing credential lands whenever the two disagree (a row
// with provider=OPENAI delivered under ANTHROPIC_API_KEY is legal today and
// lands under ANTHROPIC), and that is a behaviour change for credentials that
// work.
//
// CURSOR and FACTORY keep their env-var arms and deliberately have no llmroute
// spec: they need CredStore counts but must not reach the reverse-proxy path,
// where a provider with no descriptor would previously have been forwarded
// upstream unauthenticated.
func credTypeToProvider(c Credential) string {
	// EXCEPT for a provider whose upstream comes from the credential, which the
	// env-var switch must never claim. AddCredential accepts any syntactically
	// valid env_var_name for any provider, so an OPENAI_COMPAT credential
	// carrying OPENAI_API_KEY resolved to OPENAI here — while buildSidecarCreds
	// kept its BaseURL and Headers. The sidecar routes OPENAI to a fixed host,
	// ignores the BaseURL, and the operator's gateway token is sent to
	// api.openai.com. The wrong upstream receives a working credential, which is
	// worse than any delivery failure.
	//
	// This is not the behaviour change the ordering above warns about: no
	// credential predating this change can carry an UpstreamFromCredential
	// provider, because that provider value did not exist.
	if s, ok := llmroute.LookupProvider(c.Provider); ok && s.UpstreamFromCredential {
		return s.ID
	}
	if p := credProviderFromEnvVar(c.EnvVarName); p != "" {
		return p
	}
	if s, ok := llmroute.LookupProvider(c.Provider); ok {
		return s.ID
	}
	return ""
}

// credProviderFromEnvVar is the pre-phase-2 mapping, verbatim: a credential's
// agent-facing variable name to the sidecar provider that serves it.
func credProviderFromEnvVar(envVar string) string {
	switch envVar {
	case "ANTHROPIC_API_KEY":
		return "ANTHROPIC"
	case "OPENAI_API_KEY":
		return "OPENAI"
	case "GOOGLE_API_KEY", "GEMINI_API_KEY":
		// gemini-cli accepts either GOOGLE_API_KEY or GEMINI_API_KEY; both
		// map to the same sidecar provider type.
		return "GOOGLE"
	case "CURSOR_API_KEY":
		return "CURSOR"
	case "FACTORY_API_KEY":
		return "FACTORY"
	default:
		return ""
	}
}

// memoryPrepPaths returns every .memory directory the shared sidecar
// may serve for this crew: the requesting agent's BasePath, the crew
// shared path, and each crew member's own tier (siblings of BasePath
// under the same agents root). Deduplicated, order-stable.
func memoryPrepPaths(memoryCfg *SidecarMemoryConfig, crewMembers []SidecarCrewMember) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	add(memoryCfg.BasePath)
	add(memoryCfg.CrewMemoryPath)
	agentsRoot := path.Dir(path.Dir(memoryCfg.BasePath))
	for _, m := range crewMembers {
		if m.Slug == "" {
			continue
		}
		add(path.Join(agentsRoot, m.Slug, ".memory"))
	}
	return paths
}

// prepMemoryDirs runs the one-shot exec that pre-creates memory
// directories with shared-write perms. The agent home
// `/crew/agents/{slug}` and the crew share `/crew/shared` are
// bind-mounted at chown 1001:1001 mode 0755 by the docker init
// container — the sidecar (UID 1002) can't MkdirAll into either path
// because group/other lack write, and any pre-existing `.memory`
// subdir inherits the same restrictive perms.
//
// The exec runs as user=1001 group=1002 — NOT root. Agent containers
// drop ALL capabilities, so a root exec cannot chown (no CAP_CHOWN) —
// the original #530 prep silently failed on every cap-dropped
// container ("Operation not permitted" swallowed by the per-path
// `|| true`; verified live on dev2 2026-07-02). uid 1001 needs no
// capability for any of this: it owns the tree, mkdir inherits
// 1001:1002 from the exec identity, POSIX lets an owner chgrp to any
// group in the process's group set, and chmod is an owner right.
//
// The script, per path:
//   - pre-creates the `.memory` directory (owned 1001:1002 by the
//     exec identity)
//   - chgrps existing content to group 1002 (sidecar)
//   - applies setgid + g+rwx so new files/dirs inherit group 1002 with
//     the container entrypoint's umask 0002 making them g+rw
//
// Both UIDs can then read+write the FTS5 SQLite index and plaintext
// markdown tier files (#530). The `-R` also re-normalizes group of
// files the agent created since the last prep, keeping the dual-writer
// (agent file edits + sidecar memory.write) arrangement healthy across
// runs. Best-effort: failures are logged but never block the run —
// without these perms the path-validator fallback path still works for
// boot-context recall.
//
// Called from startSidecar (all crew members) and from the sidecar
// REUSE branch in orchestrator_run.go (the incoming agent's paths) —
// before CRE-137 the prep ran only for whichever agent started the
// sidecar, so every other member's tier kept locked-down perms.
func prepMemoryDirs(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	paths []string,
	logger *slog.Logger,
) {
	if len(paths) == 0 {
		return
	}
	// Per-path subshell `|| true` so a failure on one path doesn't
	// block prep on the next. `mkdir -p -- "..."` quotes the path so
	// unusual characters can't break the script — paths today come
	// from server config but defensive quoting is cheap. chgrp -R may
	// EPERM on individual sidecar-owned files (uid 1002); those are
	// already group 1002, so that failure mode is benign — hence `;`
	// between the steps rather than `&&` (a partial chgrp must not
	// skip the chmod).
	var prepScript strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&prepScript,
			`(mkdir -p -- "%s"; chgrp -R 1002 -- "%s"; chmod -R u+rwX,g+rwXs -- "%s") || true`+"\n",
			p, p, p)
	}
	prepCfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", prepScript.String()},
		User:        "1001:1002",
	}
	prepResult, prepErr := container.Exec(ctx, prepCfg)
	if prepErr != nil {
		logger.Warn("memory dir perms prep exec failed (run continues)", "error", prepErr)
		return
	}
	// Drain the reader first so the docker stream closes, then
	// inspect for a non-zero exit. `|| true` per path keeps the
	// script exit at 0 in normal partial-failure cases; a
	// non-zero here means a deeper docker-exec failure worth
	// surfacing (shell missing, sh -c rejected, etc.).
	var prepOut bytes.Buffer
	if prepResult != nil && prepResult.Reader != nil {
		_, _ = io.Copy(&prepOut, prepResult.Reader)
		_ = prepResult.Reader.Close()
	}
	if prepResult != nil && prepResult.ExecID != "" {
		if _, code, ierr := container.ExecInspect(ctx, prepResult.ExecID); ierr != nil {
			logger.Debug("memory dir prep inspect failed", "error", ierr)
		} else if code != 0 {
			logger.Warn("memory dir perms prep exited non-zero (run continues)",
				"exit_code", code, "output", strings.TrimSpace(prepOut.String()))
		}
	}
}
