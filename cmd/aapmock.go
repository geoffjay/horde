package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/aap"
)

// aapMockCmd is a hidden subcommand that runs the AAP mock adapter over the
// stdio binding. It is a conformance fixture: hosts can spawn it as a
// subprocess to exercise the AAP invocation path end to end.
var aapMockCmd = &cobra.Command{
	Use:    "aap-mock",
	Short:  "Run the AAP mock adapter (conformance fixture)",
	Hidden: true,
	RunE:   runAAPMock,
}

func init() {
	rootCmd.AddCommand(aapMockCmd)
}

// runAAPMock speaks the AAP stdio binding on this process's stdin/stdout. It
// refuses non-stdio transports, since the mock only implements the mandatory
// baseline binding.
func runAAPMock(_ *cobra.Command, _ []string) error {
	if transport := aap.TransportFromEnv(os.LookupEnv); transport != aap.TransportStdio {
		return fmt.Errorf("aap-mock supports only the %q transport, got %q",
			aap.TransportStdio, transport)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return aap.RunMockAdapter(ctx, os.Stdin, os.Stdout)
}
