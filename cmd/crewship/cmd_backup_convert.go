package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/convert"
)

// backupConvertCmd is offline: it reads a bundle file and writes a new one,
// with no server, no login and no workspace. It is the maintained path that
// keeps a bundle older than the direct-read window recoverable — restore,
// verify and inspect print this exact command when they meet one.
var backupConvertCmd = &cobra.Command{
	Use:   "convert",
	Short: "Convert an older backup bundle to the current format (offline; the original is never modified)",
	Long: `Convert a backup bundle written by an older Crewship to the current
format version, so the current restore can read it. Runs locally on the
bundle file — no server or login needed.

The original bundle is only read. A NEW bundle is written to --out
(default: next to the original, with a .v<N> infix) and sealed to the same
age recipients recorded in the original, or to the passphrase that opened
it, unless --recipient names new ones. The report lists every conversion
step, what it changed, and what the older format never carried and so
cannot be recovered. Use the global -f json for a machine-readable report.

Examples:
  crewship backup convert --bundle old.tar.zst --identity ~/.crewship/backup.key
  crewship backup convert --bundle old.tar.zst --out new.tar.zst --passphrase-file pass.txt
  crewship backup convert --bundle old.tar.zst --identity key.txt --recipient age1…`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		bundle, _ := cmd.Flags().GetString("bundle")
		out, _ := cmd.Flags().GetString("out")
		identityFile, _ := cmd.Flags().GetString("identity")
		passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
		recipientKeys, _ := cmd.Flags().GetStringArray("recipient")
		if bundle == "" {
			return errors.New("--bundle is required")
		}
		if identityFile != "" && passphraseFile != "" {
			return errors.New("supply only one of --identity or --passphrase-file")
		}
		if out == "" {
			out = backup.ConvertedBundlePath(bundle)
		}

		opts := convert.Options{BundlePath: bundle, OutPath: out, ConverterVersion: version}
		if identityFile != "" {
			ids, err := convert.ParseIdentityFile(identityFile)
			if err != nil {
				return err
			}
			opts.Identities = ids
		}
		if passphraseFile != "" {
			p, err := readPassphrase(passphraseFile, false)
			if err != nil {
				return err
			}
			opts.Passphrase = p
		}
		if len(recipientKeys) > 0 {
			rs, err := convert.ParseRecipients(recipientKeys)
			if err != nil {
				return err
			}
			opts.Recipients = rs
		}

		report, err := convert.Convert(cmd.Context(), opts)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(report, func() {
			fmt.Print(convert.Describe(report))
		})
	},
}

func init() {
	backupConvertCmd.Flags().String("bundle", "", "Path to the bundle to convert (required; only read, never modified)")
	backupConvertCmd.Flags().String("out", "", "Where to write the converted bundle (default: next to --bundle with a .v<N> infix); refuses to overwrite")
	backupConvertCmd.Flags().String("identity", "", "age identity file (AGE-SECRET-KEY-1…) that opens the bundle")
	backupConvertCmd.Flags().String("passphrase-file", "", "Read the passphrase that opens the bundle from a file")
	backupConvertCmd.Flags().StringArray("recipient", nil, "Seal the converted bundle to this age public key (age1…) instead of the original's recipients; repeatable")
	backupCmd.AddCommand(backupConvertCmd)
}
