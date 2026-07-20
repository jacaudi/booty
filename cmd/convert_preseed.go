package main

import (
	"fmt"
	"io"
	"os"

	bootyHTTP "github.com/jeefy/booty/pkg/http"
	"github.com/spf13/cobra"
)

// newConvertPreseedCmd builds `booty convert-preseed [FILE]`: it reads a flat
// Debian d-i preseed (FILE or stdin) and writes the equivalent structured
// debianconfig YAML to stdout, with round-trip warnings on stderr.
func newConvertPreseedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "convert-preseed [FILE]",
		Short: "Convert a flat Debian preseed into structured debianconfig YAML",
		Long: "Reads a flat Debian d-i preseed (FILE, or stdin when no FILE) and emits the " +
			"equivalent structured debianconfig YAML on stdout. Anything that cannot be mapped " +
			"onto a structured field is preserved verbatim under raw_preseed. Round-trip warnings " +
			"are written to stderr.",
		Example: "  booty convert-preseed preseed.cfg > debian.yaml\n  booty convert-preseed < preseed.cfg",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, argv []string) error {
			in := cmd.InOrStdin()
			if len(argv) == 1 {
				f, err := os.Open(argv[0])
				if err != nil {
					return fmt.Errorf("open preseed: %w", err)
				}
				defer f.Close()
				in = f
			}
			return runConvertPreseed(in, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runConvertPreseed is the pure, testable core of the subcommand: read all of
// in, convert, write YAML to out and any warnings to errOut. It returns an
// error only on an unreadable input or an internal marshaling failure — a
// round-trip mismatch is a warning, not an error (design §5).
func runConvertPreseed(in io.Reader, out, errOut io.Writer) error {
	src, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read preseed: %w", err)
	}
	yaml, warnings, err := bootyHTTP.ConvertPreseedToDebianConfig(src)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(errOut, "warning:", w)
	}
	_, err = out.Write(yaml)
	return err
}
