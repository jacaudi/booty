package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jeefy/booty/pkg/auth"
	"github.com/spf13/cobra"
)

// defaultDataDir is the one authoritative default for --dataDir, shared by the
// root command and the token subcommands. Two literals would silently diverge:
// `booty token print` would then look in a different place than the running
// server writes to.
const defaultDataDir = "/data"

// newTokenCmd builds the `booty token` admin subcommand group. It is a
// one-off admin process against the same dataDir the server uses (12-Factor
// XII), so it takes its own --dataDir rather than depending on the root
// command's flags, which are per-command and not persistent.
func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Inspect or rotate the shared API token",
		Long: "Print the API token booty authenticates with, or rotate it.\n" +
			"Rotating invalidates every outstanding UI session, because the\n" +
			"session cookie is signed with a key derived from the token.",
		Example: "  booty token print --dataDir=/data\n  booty token rotate --yes --dataDir=/data",
	}
	cmd.AddCommand(newTokenPrintCmd(), newTokenRotateCmd())
	return cmd
}

func newTokenPrintCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "print",
		Short: "Print the current API token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := auth.NewStore(tokenPath(dataDir))
			if err := store.Reload(); err != nil {
				return fmt.Errorf("no API token at %s (start booty once to generate one, or pass --apiToken): %w",
					store.Path(), err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), store.Token().Expose())
			return err
		},
	}
	cmd.Flags().StringVar(&dataDir, "dataDir", defaultDataDir, "Directory holding the api-token file")
	return cmd
}

func newTokenRotateCmd() *cobra.Command {
	var (
		dataDir string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Generate and persist a new API token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("rotate logs out every UI session and invalidates the old token; re-run with --yes to confirm")
			}
			store := auth.NewStore(tokenPath(dataDir))
			tok, err := store.Rotate()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), tok.Expose())
			return err
		},
	}
	cmd.Flags().StringVar(&dataDir, "dataDir", defaultDataDir, "Directory holding the api-token file")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the rotation without prompting")
	return cmd
}

func tokenPath(dataDir string) string { return filepath.Join(dataDir, auth.TokenFileName) }
