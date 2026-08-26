package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/llmroute"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// securityLevelHelp lists the tiers and what each one means. Generated from
// keeper.SecurityLevels rather than restated, so the flag help, the rejection
// message and the policy that enforces them cannot drift into describing three
// different scales.
func securityLevelHelp() string {
	parts := make([]string, 0, len(keeper.SecurityLevels()))
	for _, l := range keeper.SecurityLevels() {
		parts = append(parts, fmt.Sprintf("%d = %s", int(l), l.Label()))
	}
	return strings.Join(parts, ", ")
}

// buildEndpointCredentialValue folds an ENDPOINT_URL base URL plus an optional
// bearer token and repeatable `K=V` headers into the one-object JSON the server
// stores (#961). The token/headers never appear in the plaintext value shown by
// `credential list`. Returns the compact JSON string.
// readAuthToken resolves the endpoint bearer token from --auth-token or
// --auth-token-stdin, mirroring the --value/--value-stdin pair that already
// exists for the credential's main value.
//
// A secret passed as a command-line argument is readable by anything that can
// see the process table for as long as the command runs, and lands in the
// operator's shell history besides. --value has had a stdin path since it was
// added; --auth-token did not, so the documented way to rotate an endpoint key
// was the insecure one. Returns the token and whether the caller supplied one
// at all — rotate needs the distinction, because "not sent" and "sent empty"
// mean different things to the server-side merge.
func readAuthToken(flags *pflag.FlagSet) (string, bool, error) {
	token, _ := flags.GetString("auth-token")
	fromStdin, _ := flags.GetBool("auth-token-stdin")
	if fromStdin {
		if flags.Changed("auth-token") {
			return "", false, cli.WithExitCode(
				fmt.Errorf("--auth-token and --auth-token-stdin are mutually exclusive"), cli.ExitValidation)
		}
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			token = scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			return "", false, cli.WithExitCode(
				fmt.Errorf("read --auth-token-stdin: %w", err), cli.ExitValidation)
		}
		// Empty stdin is a mistake, not an instruction to clear the token.
		// --value-stdin already fails closed on this, and the asymmetry was a
		// footgun with a silent, destructive outcome: a mistyped pipe that
		// produced no bytes would send an empty token, the server would merge it
		// over the stored one, and the credential would be left authenticating
		// with nothing. Clearing a token deliberately is still possible and now
		// has to be said out loud, with --auth-token "".
		if token == "" {
			return "", false, cli.WithExitCode(
				fmt.Errorf("--auth-token-stdin got no input; pass --auth-token \"\" if you really mean to clear the stored token"),
				cli.ExitValidation)
		}
		return token, true, nil
	}
	return token, flags.Changed("auth-token"), nil
}

func buildEndpointCredentialValue(baseURL, authToken string, headerPairs []string) (string, error) {
	headers := map[string]string{}
	for _, hp := range headerPairs {
		k, v, ok := strings.Cut(hp, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return "", fmt.Errorf("--header must be KEY=VALUE, got %q", hp)
		}
		headers[k] = strings.TrimSpace(v)
	}
	if authToken == "" && len(headers) == 0 {
		return strings.TrimSpace(baseURL), nil
	}
	obj := map[string]interface{}{"baseURL": strings.TrimSpace(baseURL)}
	if authToken != "" {
		obj["apiKey"] = authToken
	}
	if len(headers) > 0 {
		obj["headers"] = headers
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// routeEndpointProvider reports whether a --provider value names a sidecar
// route whose UPSTREAM comes out of the credential itself — today that is
// OPENAI_COMPAT, a self-hosted or vendor OpenAI-compatible gateway.
//
// Those credentials carry {baseURL,apiKey,headers} as ONE object for the same
// reason #961's ENDPOINT_URL does: the sidecar has nowhere to send the request
// without the URL, and the URL is worthless to it without the key. Splitting
// them across two credentials would make either half deliverable on its own.
//
// Matched case-insensitively — `credential create --provider openai_compat`
// and `--provider OPENAI_COMPAT` are the same provider — and normalized to the
// registry's UPPERCASE id before it reaches the server, which stores the
// provider column verbatim.
func routeEndpointProvider(provider string) (llmroute.Spec, bool) {
	spec, ok := llmroute.Lookup(strings.ToUpper(strings.TrimSpace(provider)))
	if !ok || !spec.UpstreamFromCredential {
		return llmroute.Spec{}, false
	}
	return spec, true
}

// routeEndpointProviderIDs lists those providers for an error message, so a
// rejected --base-url says which provider it WOULD have been valid for rather
// than only that it was wrong.
func routeEndpointProviderIDs() []string {
	out := []string{}
	for _, s := range llmroute.Specs() {
		if s.UpstreamFromCredential {
			out = append(out, s.ID)
		}
	}
	return out
}

// endpointCredentialTypes are the two types a credential-supplied endpoint may
// be stored as. API_KEY is the one phase 2 delivers to the sidecar's CredStore;
// ENDPOINT_URL is the pre-existing #961 shape and is accepted so an operator
// who already has one is not told their own credential is malformed.
//
// Any other type would be stored happily by the server and then never routed —
// credpolicy resolves delivery by TYPE, and a SECRET carrying a base URL
// reaches an agent's environment rather than the sidecar, which is the exact
// leak this provider exists to close.
var endpointCredentialTypes = map[string]bool{"API_KEY": true, "ENDPOINT_URL": true}

// resolveCrewIDs resolves --crews values (crew slug or ID, in any order) to
// crew IDs — the form the API's credential_crews junction expects. Blank
// entries are dropped so `--crews ""` clears the scoping (workspace-wide).
func resolveCrewIDs(client *cli.Client, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		id, err := resolveCrewID(client, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve crew %q: %w", ref, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// normalizeCredentialScope validates a --scope flag value against the
// server's WORKSPACE|CREW enum, case-insensitively, and normalizes it to
// upper case. An empty input is passed through unchanged — an absent
// --scope means "let the server infer it from --crews". Anything else is a
// clear client-side error rather than a value that lands in the DB and
// half-orphans the credential (#1083).
func normalizeCredentialScope(scope string) (string, error) {
	if scope == "" {
		return "", nil
	}
	switch strings.ToUpper(scope) {
	case "WORKSPACE":
		return "WORKSPACE", nil
	case "CREW":
		return "CREW", nil
	default:
		return "", fmt.Errorf("--scope must be WORKSPACE or CREW, got %q", scope)
	}
}

var credCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a credential",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		credType, _ := flags.GetString("type")
		provider, _ := flags.GetString("provider")
		value, _ := flags.GetString("value")
		valueStdin, _ := flags.GetBool("value-stdin")
		envVarName, _ := flags.GetString("env-var-name")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if credType == "" {
			return fmt.Errorf("--type is required (SECRET, API_KEY, AI_CLI_TOKEN, or CLI_TOKEN)")
		}

		// #2086: an OAUTH2 credential is created empty — the row exists so the
		// connect flow has somewhere to put the tokens it fetches. It carries
		// the OAuth *app* details instead of a value, so the two checks that
		// assume a value (--value required, and the provider key probe) are
		// skipped for it below. Resolved here, before anything is sent, so a
		// typo'd provider slug never becomes a credential with no authorize URL.
		oauthApp, oauthErr := readOAuthAppFlags(flags, credType)
		if oauthErr != nil {
			return oauthErr
		}

		if valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = scanner.Text()
			}
		}

		authToken, _, err := readAuthToken(flags)
		if err != nil {
			return err
		}
		headerPairs, _ := flags.GetStringArray("header")
		baseURL, _ := flags.GetString("base-url")
		endpointSpec, providerCarriesEndpoint := routeEndpointProvider(provider)

		// A provider whose upstream comes from the credential (OPENAI_COMPAT)
		// takes its endpoint on --base-url and its key on --value/--auth-token,
		// and the two are stored as one object — the same shape #961 already
		// defined for ENDPOINT_URL, so nothing new had to be invented on the
		// server side.
		switch {
		case baseURL != "" && !providerCarriesEndpoint:
			return cli.WithExitCode(fmt.Errorf(
				"--base-url is only valid for a provider whose endpoint comes from the credential (%s); every other provider has a fixed upstream the sidecar already knows",
				strings.Join(routeEndpointProviderIDs(), ", ")), cli.ExitValidation)

		case providerCarriesEndpoint && baseURL == "":
			return cli.WithExitCode(fmt.Errorf(
				"--base-url is required for --provider %s: without it the sidecar has no upstream to forward to",
				endpointSpec.ID), cli.ExitValidation)

		case providerCarriesEndpoint:
			if !endpointCredentialTypes[credType] {
				return cli.WithExitCode(fmt.Errorf(
					"--provider %s needs --type API_KEY (or ENDPOINT_URL); --type %s is delivered to the agent's environment instead of the sidecar, which would put the key in the container",
					endpointSpec.ID, credType), cli.ExitValidation)
			}
			// The key may come the ordinary way (--value/--value-stdin) or on
			// --auth-token, which is what an operator who already writes
			// ENDPOINT_URL credentials will reach for.
			key := authToken
			if key == "" {
				key = value
			}
			// A bearer token is one kind of auth material, not the only kind:
			// an endpoint that authenticates on `X-Api-Key: …` has no token to
			// give, and llmroute.ApplyAuth writes custom headers independently
			// of the token. Requiring a key here made that endpoint — which the
			// sidecar routes and the docs describe — impossible to create.
			if key == "" && len(headerPairs) == 0 {
				return cli.WithExitCode(
					fmt.Errorf("--value, --value-stdin, --auth-token or --header is required: an endpoint with no auth material at all is not routed through the sidecar, so there is nothing to isolate"),
					cli.ExitValidation)
			}
			v, err := buildEndpointCredentialValue(baseURL, key, headerPairs)
			if err != nil {
				// A malformed --header is a local validation failure, and the
				// exit-code contract spells those 2. It used to leave here as a
				// bare error (exit 1, "unclassified"), which tells a script that
				// retried on 1 to retry a command that can never succeed.
				return cli.WithExitCode(err, cli.ExitValidation)
			}
			value = v

		// #961: an ENDPOINT_URL credential may carry an auth token + custom
		// headers for a self-hosted or gateway-fronted OpenAI-compatible
		// endpoint that requires authentication. When either is set, fold
		// {baseURL,apiKey,headers} into the stored value as one credential
		// object; with neither it stays a bare URL.
		case authToken != "" || len(headerPairs) > 0:
			// Exit 2, like every sibling in this switch. These are argument
			// errors settled before a request is built, so a script that retries
			// on exit 1 must not be told to retry a command that can never
			// succeed — that is the whole point of the contract.
			if credType != "ENDPOINT_URL" {
				return cli.WithExitCode(fmt.Errorf(
					"--auth-token/--header are only valid with --type ENDPOINT_URL, or with a provider whose endpoint comes from the credential (%s)",
					strings.Join(routeEndpointProviderIDs(), ", ")), cli.ExitValidation)
			}
			if value == "" {
				return cli.WithExitCode(
					fmt.Errorf("--value or --value-stdin is required"), cli.ExitValidation)
			}
			v, err := buildEndpointCredentialValue(value, authToken, headerPairs)
			if err != nil {
				return cli.WithExitCode(err, cli.ExitValidation)
			}
			value = v
		}

		if value == "" && oauthApp == nil {
			return cli.WithExitCode(
				fmt.Errorf("--value or --value-stdin is required"), cli.ExitValidation)
		}

		// Normalize the provider to the registry's spelling before it is
		// stored: the sidecar looks the provider column up case-sensitively,
		// so a credential created as "openai_compat" would be routed by nothing.
		if providerCarriesEndpoint {
			provider = endpointSpec.ID
		}

		secLevel, _ := flags.GetInt("security-level")
		// 0 means "not passed" (the flag's zero value), so it stays legal and the
		// server applies its default. Anything else has to be a real tier: the
		// ceiling used to be 3, which made L4 — the tier that requires human
		// approval on every read — unreachable from the CLI.
		if secLevel != 0 && !keeper.SecurityLevel(secLevel).Valid() {
			return fmt.Errorf("--security-level %d is not a tier: %s", secLevel, securityLevelHelp())
		}

		body := map[string]interface{}{
			"name": name,
			"type": credType,
		}
		if oauthApp == nil {
			body["value"] = value
		} else {
			// No "value" key at all rather than an empty one: the server
			// substitutes its own PENDING sentinel for an OAUTH2 row with no
			// value, and inventing a placeholder here would put a second
			// spelling of "not configured yet" into the column.
			oauthApp.apply(body)
		}
		if provider != "" {
			body["provider"] = provider
		}
		if envVarName != "" {
			body["env_var_name"] = envVarName
		}
		// account_label already existed on the API and in manifests but had
		// no flag, so the one field that says "this token is for THAT forge"
		// was unreachable from the CLI. Git links resolve a credential by it
		// (internal/api/issue_code_links.go), which makes the gap a blocker
		// for any self-hosted GitHub/GitLab.
		if label, _ := flags.GetString("account-label"); label != "" {
			body["account_label"] = label
		}
		if secLevel >= 1 {
			body["security_level"] = secLevel
		}

		client := newAPIClient()

		// #1083: crew scoping parity. --crews accepts slugs or IDs; resolve
		// each to an ID (what the API's credential_crews junction expects).
		// The server derives scope=CREW from a non-empty crew_ids list, but
		// an explicit --scope still passes through for the workspace-wide case.
		var crewIDs []string
		if flags.Changed("crews") {
			crewRefs, _ := flags.GetStringSlice("crews")
			var err error
			crewIDs, err = resolveCrewIDs(client, crewRefs)
			if err != nil {
				return err
			}
			body["crew_ids"] = crewIDs
		}
		rawScope, _ := flags.GetString("scope")
		scope, err := normalizeCredentialScope(rawScope)
		if err != nil {
			return err
		}
		if scope != "" {
			body["scope"] = scope
		}
		// The server auto-sets scope=CREW whenever crew_ids is non-empty,
		// silently overriding an explicit --scope WORKSPACE. Warn rather
		// than fail the request — the server's behaviour wins either way.
		if scope == "WORKSPACE" && len(crewIDs) > 0 {
			cli.PrintWarning("--scope WORKSPACE is ignored when --crews is set; the credential will be scope=CREW")
		}

		// A credential-supplied endpoint is deliberately NOT probed. The stored
		// value is an object rather than a bare key, and crewshipd will not dial
		// an operator-supplied URL — reaching an arbitrary host from the server
		// is an SSRF surface the probe path is not built for. Saying so is the
		// honest answer; running no check and printing "validated successfully"
		// would be a green tick over a test that never happened.
		switch {
		case oauthApp != nil:
			// Nothing to probe: the row holds an OAuth app's client id, not a
			// token. The token arrives when the flow completes, and the probe
			// that would check it has nothing to send until then.

		case providerCarriesEndpoint:
			cli.PrintWarning(fmt.Sprintf(
				"%s is not validated on create — Crewship does not dial an operator-supplied endpoint. The first agent call through the sidecar is the test.",
				endpointSpec.ID))

		default:
			valid, errMsg := testCredentialValue(client, provider, credType, value)
			if valid {
				cli.PrintSuccess("Key validated successfully")
			} else {
				msg := errMsg
				if msg == "" {
					msg = "key validation failed"
				}
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					cli.PrintWarning(fmt.Sprintf("Key validation failed: %s (non-interactive, skipping confirmation)", msg))
				} else if !confirmInvalidKey(msg) {
					return fmt.Errorf("aborted")
				}
			}
		}

		resp, err := client.Post("/api/v1/credentials", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Credential created: %s (%s)", created.Name, created.ID))
		if oauthApp != nil {
			// The row exists and holds nothing yet. Saying "created" and
			// stopping would leave an operator with a credential that fails
			// every agent run until they discover the second half themselves.
			fmt.Printf("Status is PENDING until the OAuth flow completes. Finish it with:\n"+
				"  crewship oauth connect %s\n", created.Name)
		}
		return nil
	},
}

var credUpdateCmd = &cobra.Command{
	Use:   "update <name-or-id>",
	Short: "Update a credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("value") {
			v, _ := flags.GetString("value")
			if v == "" {
				return fmt.Errorf("--value cannot be empty")
			}
			body["value"] = v
		}
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("value-stdin") {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				v := scanner.Text()
				if v == "" {
					return fmt.Errorf("stdin value cannot be empty")
				}
				body["value"] = v
			}
		}
		if flags.Changed("security-level") {
			v, _ := flags.GetInt("security-level")
			if !keeper.SecurityLevel(v).Valid() {
				return fmt.Errorf("--security-level %d is not a tier: %s", v, securityLevelHelp())
			}
			body["security_level"] = v
		}
		// Passing an empty --account-label clears it, which is how a
		// credential stops being pinned to one host.
		if flags.Changed("account-label") {
			v, _ := flags.GetString("account-label")
			body["account_label"] = v
		}
		// #1083: crew scoping parity. Passing --crews (even empty, to clear)
		// replaces the credential_crews set; the server re-derives scope.
		var crewIDs []string
		crewsChanged := flags.Changed("crews")
		if crewsChanged {
			crewRefs, _ := flags.GetStringSlice("crews")
			var err error
			crewIDs, err = resolveCrewIDs(client, crewRefs)
			if err != nil {
				return err
			}
			body["crew_ids"] = crewIDs
		}
		if flags.Changed("scope") {
			rawScope, _ := flags.GetString("scope")
			if rawScope == "" {
				return fmt.Errorf("--scope cannot be empty")
			}
			scope, err := normalizeCredentialScope(rawScope)
			if err != nil {
				return err
			}
			body["scope"] = scope
			// The server auto-sets scope=CREW whenever crew_ids is non-empty,
			// silently overriding an explicit --scope WORKSPACE. Warn rather
			// than fail the request — the server's behaviour wins either way.
			if scope == "WORKSPACE" && crewsChanged && len(crewIDs) > 0 {
				cli.PrintWarning("--scope WORKSPACE is ignored when --crews is set; the credential will be scope=CREW")
			}
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		if val, ok := body["value"]; ok {
			if valStr, ok := val.(string); ok && valStr != "" {
				metaResp, metaErr := client.Get("/api/v1/credentials/" + credID)
				if metaErr != nil {
					cli.PrintWarning("Could not fetch credential metadata for validation: " + metaErr.Error())
				} else if err := cli.CheckError(metaResp); err != nil {
					cli.PrintWarning("Could not fetch credential metadata for validation: " + err.Error())
				} else {
					var cred struct {
						Type     string `json:"type"`
						Provider string `json:"provider"`
					}
					if err := cli.ReadJSON(metaResp, &cred); err != nil {
						cli.PrintWarning("Could not parse credential metadata, skipping validation: " + err.Error())
					} else if spec, ok := routeEndpointProvider(cred.Provider); ok {
						// Same as create: crewshipd does not dial an
						// operator-supplied endpoint, so there is no check to
						// report the result of. Saying nothing happened beats a
						// green tick over a probe that never ran.
						cli.PrintWarning(fmt.Sprintf(
							"%s is not validated on update — Crewship does not dial an operator-supplied endpoint.",
							spec.ID))
					} else {
						valid, errMsg := testCredentialValue(client, cred.Provider, cred.Type, valStr)
						if valid {
							cli.PrintSuccess("Key validated successfully")
						} else {
							msg := errMsg
							if msg == "" {
								msg = "key validation failed"
							}
							if !term.IsTerminal(int(os.Stdin.Fd())) {
								cli.PrintWarning(fmt.Sprintf("Key validation failed: %s (non-interactive, skipping confirmation)", msg))
							} else if !confirmInvalidKey(msg) {
								return fmt.Errorf("aborted")
							}
						}
					}
				}
			}
		}

		resp, err := client.Patch("/api/v1/credentials/"+credID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential updated.")
		return nil
	},
}

// credRotateCmd issues a new value for a credential and starts a grace
// overlap. Destructive (the old value is moved to a rotation row and
// scrubbed after the grace window expires) so it gates behind a confirm
// prompt unless --yes is passed.
//
// Flag shape mirrors `credential create`: the new value can come on the
// command line (--value, visible in `ps`) or from stdin (--value-stdin,
// preferred for scripts).
var credRotateCmd = &cobra.Command{
	Use:   "rotate <name-or-id>",
	Short: "Rotate a credential value with a grace-overlap window",
	Long: `Issue a new value for the credential. The old value is preserved
on the rotation row for --grace-seconds (default 24h, max 7d) so
in-flight agents that cached the old key can still fall back during
their run, then the old value is scrubbed.

Examples:
  crewship credential rotate gh-token --value sk_new_... --yes
  echo "$NEW" | crewship credential rotate gh-token --value-stdin
  crewship credential rotate gh-token --value-stdin --grace-seconds 0  # immediate cutover`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		value, _ := flags.GetString("value")
		valueStdin, _ := flags.GetBool("value-stdin")
		if valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = scanner.Text()
			}
		}
		rotateAuthToken, authChanged, err := readAuthToken(flags)
		if err != nil {
			return err
		}
		headerChanged := flags.Changed("header")
		rotatingEndpointAuth := authChanged || headerChanged

		// For a plain rotate we require a new value up front. For an ENDPOINT_URL
		// field rotation (--auth-token/--header) the base URL can be omitted —
		// the SERVER merges the changed field(s) over the stored value, keeping
		// the rest. The CLI must NOT hand-build a full {baseURL,apiKey,headers}
		// JSON here: it can't read the existing token/headers (secrets are never
		// returned), so building the object client-side would silently drop the
		// fields it can't see. Send only what changed and let the server merge.
		if value == "" && !rotatingEndpointAuth {
			return fmt.Errorf("--value or --value-stdin is required")
		}

		if err := confirmAction(cmd, fmt.Sprintf("Rotate credential %q? The old value will be scrubbed after the grace window.", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		if rotatingEndpointAuth {
			// Guard REGARDLESS of whether --value was passed: --auth-token/--header
			// on a non-ENDPOINT_URL credential would otherwise be misapplied (a
			// full-value rotate storing a JSON blob as, say, a GITHUB secret).
			metaResp, metaErr := client.Get("/api/v1/credentials/" + credID)
			if metaErr != nil {
				return fmt.Errorf("could not fetch credential to validate its type: %w", metaErr)
			}
			if err := cli.CheckError(metaResp); err != nil {
				return err
			}
			var cred struct {
				Type     string `json:"type"`
				Provider string `json:"provider"`
			}
			if err := cli.ReadJSON(metaResp, &cred); err != nil {
				return err
			}
			// The question is whether this credential STORES an endpoint
			// object, not whether its type is ENDPOINT_URL. A provider whose
			// upstream comes from the credential (OPENAI_COMPAT) stores the
			// same {baseURL,apiKey,headers} shape under type API_KEY, and
			// refusing the field-by-field form for it would leave a full-value
			// rotate — which replaces the endpoint along with the key — as the
			// only way to change that key.
			_, providerCarriesEndpoint := routeEndpointProvider(cred.Provider)
			if cred.Type != "ENDPOINT_URL" && !providerCarriesEndpoint {
				return fmt.Errorf("--auth-token/--header are only valid when rotating a credential that stores an endpoint: type ENDPOINT_URL, or a provider whose endpoint comes from the credential (%s). Got type %s, provider %s",
					strings.Join(routeEndpointProviderIDs(), ", "), cred.Type, cred.Provider)
			}
			// Send only the changed field(s); the server merges over the stored
			// value so unspecified fields (headers when rotating the token, or
			// vice versa) are preserved.
			if value != "" {
				body["endpoint_base_url"] = value
			}
			if authChanged {
				body["endpoint_auth_token"] = rotateAuthToken
			}
			if headerChanged {
				headerPairs, _ := flags.GetStringArray("header")
				headers := map[string]string{}
				for _, hp := range headerPairs {
					k, v, ok := strings.Cut(hp, "=")
					k = strings.TrimSpace(k)
					if !ok || k == "" {
						return fmt.Errorf("--header must be KEY=VALUE, got %q", hp)
					}
					headers[k] = strings.TrimSpace(v)
				}
				body["endpoint_headers"] = headers
			}
		} else {
			body["value"] = value
		}
		if flags.Changed("grace-seconds") {
			gs, _ := flags.GetInt("grace-seconds")
			if gs < 0 || gs > 604800 {
				return fmt.Errorf("--grace-seconds must be between 0 and 604800 (7 days)")
			}
			body["grace_seconds"] = gs
		}

		resp, err := client.Post("/api/v1/credentials/"+credID+"/rotate", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			GraceSeconds int    `json:"grace_seconds"`
			ExpiresAt    string `json:"expires_at"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf(
			"Rotation %s started (grace %ds, expires %s)",
			out.ID, out.GraceSeconds, out.ExpiresAt,
		))
		return nil
	},
}

// credRotationCancelCmd ends an ACTIVE grace window immediately and
// scrubs the old value. EXPIRED / CANCELLED rotations are no-ops on
// the server side (idempotent 200), so the command still succeeds.
var credRotationCancelCmd = &cobra.Command{
	Use:   "rotation-cancel <rotation-id>",
	Short: "End an ACTIVE rotation's grace window early (scrubs old value)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Cancel rotation %q? The old value will be scrubbed immediately.", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/credential-rotations/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		switch {
		case out.Message != "":
			cli.PrintSuccess(fmt.Sprintf("Rotation %s: %s (%s)", args[0], out.Status, out.Message))
		case out.Status != "" && !strings.EqualFold(out.Status, "cancelled"):
			cli.PrintSuccess(fmt.Sprintf("Rotation %s: %s", args[0], out.Status))
		default:
			cli.PrintSuccess(fmt.Sprintf("Rotation %s cancelled.", args[0]))
		}
		return nil
	},
}

var credDeleteCmd = &cobra.Command{
	Use:   "delete <name-or-id>",
	Short: "Delete a credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete credential %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/credentials/" + credID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential deleted.")
		return nil
	},
}
