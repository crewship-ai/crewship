package main

// CLI parity for the credential custom-field surface
// (PRD-CREDENTIALS-V2-2026 §2.2, project rule #3: every /api/v1 route gets a
// matching command).
//
// A credential used to be one value plus an optional username, which cannot
// express AWS static credentials, a service-account JSON, or anything with a
// TOTP seed or a passphrase. These three commands are how an operator — or an
// agent driving the CLI — puts the extra parts in and takes them out again.
//
// What they deliberately cannot do is READ a secret part back. The server does
// not return it, so there is nothing here to print; `crewship credential
// reveal` is the one path that discloses a stored value, and it demands the
// full §2.6 ceremony. A `field get --show` would have been a second, quieter
// disclosure endpoint wearing a different name.

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var credFieldCmd = &cobra.Command{
	Use:     "field",
	Aliases: []string{"fields"},
	Short:   "Manage a credential's custom fields",
	Long: `Custom fields hold the parts of a credential that do not fit in a single
value: an AWS access key id next to its secret and region, a service-account
filename next to its blob, a TOTP seed next to a password.

Each field is either SECRET (encrypted at rest, never shown again) or PLAIN
(--plain: stored in cleartext and displayed). Plain is for identifiers —
region, account id, host — which are not secrets and which the UI needs to be
able to search and sort. When in doubt do not pass --plain: the default is
secret.

The credential's own value and username are NOT fields. Set them with
'crewship credential update'; the keys "value", "password" and "username" are
reserved here so one datum can never end up with two copies that drift apart.`,
}

// credentialFieldKeyRe mirrors the server's gate so a typo fails before the
// request instead of coming back as a 400 the operator has to map onto their
// input. The server remains the authority — this is the fast, honest error.
var credentialFieldKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// credFieldRow is one row of GET /credentials/{id}/fields. Value is a pointer
// and is null for every secret field — that is the server's contract, not a
// client-side redaction, so there is no way for this struct to hold a secret.
type credFieldRow struct {
	Key       string  `json:"key" yaml:"key"`
	IsSecret  bool    `json:"is_secret" yaml:"is_secret"`
	Ordinal   int     `json:"ordinal" yaml:"ordinal"`
	Value     *string `json:"value" yaml:"value"`
	UpdatedAt string  `json:"updated_at" yaml:"updated_at"`
}

var credFieldListCmd = &cobra.Command{
	Use:   "list <credential>",
	Short: "List a credential's custom fields",
	Long: `List the custom fields on a credential.

Secret fields show their key and the marker (secret); their value is not
returned by the server and cannot be printed here. Plain fields show their
value, which is what they are for.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		var rows []credFieldRow
		if err := getJSON(client, "/api/v1/credentials/"+credID+"/fields", &rows); err != nil {
			return err
		}
		if len(rows) == 0 {
			return emptyListNote(cmd, rows, "No custom fields on this credential.")
		}

		f := newFormatter()
		headers := []string{"KEY", "KIND", "VALUE"}
		var table [][]string
		for _, r := range rows {
			kind, value := "plain", ""
			if r.IsSecret {
				// Never a fixed-width mask: "••••••••" invites the reader to
				// infer a length that isn't there, and a length is the first
				// half of a guess.
				kind, value = "secret", "(secret)"
			} else if r.Value != nil {
				value = *r.Value
			}
			table = append(table, []string{r.Key, kind, value})
		}
		return f.Auto(rows, headers, table)
	},
}

var credFieldSetCmd = &cobra.Command{
	Use:   "set <credential> <key>",
	Short: "Add or replace one custom field",
	Long: `Add a custom field, or replace the value of one that already exists.

The value is secret unless --plain is passed. Prefer --value-stdin for a
secret: an argument on the command line lands in your shell history and in
'ps' output for as long as the process runs.

Examples:
  crewship credential field set aws-prod region --value eu-central-1 --plain
  crewship credential field set aws-prod secret_access_key --value-stdin < key.txt
  crewship credential field set gmail totp_seed --value-stdin`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := strings.TrimSpace(args[1])
		if !credentialFieldKeyRe.MatchString(key) {
			return fmt.Errorf("field key %q must be lower_snake_case, start with a letter, and contain "+
				"only a-z, 0-9 and _ — it becomes an environment-variable name and a file name when the "+
				"credential is delivered, where Region and region would collide", args[1])
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
		if value == "" {
			return fmt.Errorf("--value or --value-stdin is required")
		}
		plain, _ := flags.GetBool("plain")

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]any{"key": key, "value": value, "is_secret": !plain}
		if flags.Changed("ordinal") {
			ord, _ := flags.GetInt("ordinal")
			body["ordinal"] = ord
		}

		// Create first. The server answers 409 when the key is taken, which is
		// the honest API — a POST that silently overwrote somebody else's value
		// would be a data-loss bug wearing an idempotent face. The CLI is
		// allowed to be friendlier than the API, so it upgrades the 409 into
		// the update the operator obviously meant.
		resp, err := client.Post("/api/v1/credentials/"+credID+"/fields", body)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusConflict {
			_ = resp.Body.Close()
			resp, err = client.Put("/api/v1/credentials/"+credID+"/fields/"+key, body)
			if err != nil {
				return err
			}
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out credFieldRow
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		kind := "secret"
		if !out.IsSecret {
			kind = "plain"
		}
		cli.PrintSuccess(fmt.Sprintf("Field %q set (%s)", out.Key, kind))
		if out.IsSecret {
			fmt.Fprintln(cmd.OutOrStdout(),
				"The value is encrypted at rest and will not be shown again.")
		}
		return nil
	},
}

var credFieldRemoveCmd = &cobra.Command{
	Use:     "remove <credential> <key>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove one custom field",
	Long: `Remove a custom field from a credential.

Removing a field that is not there is an error, not a silent success — when
you are revoking something you need to know whether it was ever present.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		key := strings.TrimSpace(args[1])

		resp, err := client.Delete("/api/v1/credentials/" + credID + "/fields/" + key)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Field %q removed", key))
		return nil
	},
}

func init() {
	credFieldSetCmd.Flags().String("value", "", "Field value (prefer --value-stdin for secrets)")
	credFieldSetCmd.Flags().Bool("value-stdin", false, "Read the field value from stdin")
	credFieldSetCmd.Flags().Bool("plain", false,
		"Store in cleartext — for identifiers (region, account id, host), never for secrets")
	credFieldSetCmd.Flags().Int("ordinal", 0, "Display position (defaults to appending)")

	credFieldCmd.AddCommand(credFieldListCmd)
	credFieldCmd.AddCommand(credFieldSetCmd)
	credFieldCmd.AddCommand(credFieldRemoveCmd)
	credentialCmd.AddCommand(credFieldCmd)
}
