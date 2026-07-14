package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/aap"
	"github.com/geoffjay/horde/internal/config"
)

var (
	aapRunAgent   string
	aapRunCommand string
	aapRunArgs    []string
	aapRunPrompt  string
	aapRunCwd     string
	aapRunModel   string
)

// aapRunCmd is a hidden subcommand that drives an external AAP agent adapter
// through a single turn over the stdio binding. It is the host counterpart to
// the aap-mock adapter: where aap-mock proves the adapter side, aap-run proves
// the node's host-side driver against a real adapter (e.g. the pi adapter).
var aapRunCmd = &cobra.Command{
	Use:    "aap-run",
	Short:  "Drive an external AAP agent adapter through one turn",
	Hidden: true,
	RunE:   runAAPRun,
}

func init() {
	f := aapRunCmd.Flags()
	f.StringVar(&aapRunAgent, "agent", "", "adapter name from the `adapters` config section")
	f.StringVar(&aapRunCommand, "command", "", "adapter command (ad-hoc; overrides --agent)")
	f.StringArrayVar(&aapRunArgs, "arg", nil, "adapter argument, repeatable (used with --command)")
	f.StringVar(&aapRunPrompt, "prompt", "", "prompt to send (required)")
	f.StringVar(&aapRunCwd, "cwd", "", "workspace cwd (default: current directory)")
	f.StringVar(&aapRunModel, "model", "", "AAP model string (default: the adapter's configured default)")
	rootCmd.AddCommand(aapRunCmd)
}

func runAAPRun(_ *cobra.Command, _ []string) error {
	if aapRunPrompt == "" {
		return errors.New("--prompt is required")
	}

	command, args, env, model, err := resolveAdapter()
	if err != nil {
		return err
	}
	if aapRunModel != "" {
		model = aapRunModel
	}

	cwd := aapRunCwd
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return fmt.Errorf("resolve cwd: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sess, cmd, err := spawnAdapter(ctx, command, args, env)
	if err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill() }()

	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	ready, err := sess.Initialize(&aap.Initialize{
		Model:     modelPtr,
		Workspace: aap.Workspace{Cwd: cwd},
	})
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	fmt.Fprintf(os.Stderr, "ready: agent=%s capabilities=[%s]\n",
		ready.Agent.Name, strings.Join(ready.Capabilities, ","))

	tc, err := sess.Prompt("t1", aap.TextPrompt(aapRunPrompt), turnObserver())
	if err != nil {
		return fmt.Errorf("turn: %w", err)
	}
	fmt.Println() // terminate the streamed line
	printTurnSummary(tc)

	_ = sess.Shutdown()
	_ = cmd.Wait()

	if tc.IsError {
		return errors.New("turn ended with an error (see log above)")
	}
	return nil
}

// spawnAdapter starts the adapter process with the AAP stdio transport env set
// and returns a host session bound to its stdio pipes.
func spawnAdapter(
	ctx context.Context, command string, args []string, env map[string]string,
) (*aap.HostSession, *exec.Cmd, error) {
	// command comes from operator-controlled config/flags, not untrusted input.
	cmd := exec.CommandContext(ctx, command, args...) //#nosec G204
	cmd.Env = append(os.Environ(), aap.EnvTransport+"="+aap.TransportStdio)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("adapter stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("adapter stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start adapter %q: %w", command, err)
	}
	return aap.NewHostSession(stdout, stdin), cmd, nil
}

// resolveAdapter selects the adapter command from --command or the named
// entry in the `adapters` config section.
func resolveAdapter() (command string, args []string, env map[string]string, model string, err error) {
	if aapRunCommand != "" {
		return aapRunCommand, aapRunArgs, nil, "", nil
	}
	if aapRunAgent == "" {
		return "", nil, nil, "", errors.New("one of --agent or --command is required")
	}
	cfg := config.Get()
	a, ok := cfg.Adapters[aapRunAgent]
	if !ok {
		return "", nil, nil, "", fmt.Errorf("no adapter %q configured (adapters.%s)", aapRunAgent, aapRunAgent)
	}
	if a.Command == "" {
		return "", nil, nil, "", fmt.Errorf("adapter %q has no command", aapRunAgent)
	}
	return a.Command, a.Args, a.Env, a.Model, nil
}

// turnObserver streams assistant output to stdout, diagnostics to stderr, and
// auto-allows tool approvals (this is an unattended one-shot driver).
func turnObserver() aap.TurnObserver {
	return aap.TurnObserver{
		OnMessage: func(m aap.Message) {
			for _, b := range m.Content {
				if b.Type == aap.BlockText {
					fmt.Print(b.Text)
				}
			}
		},
		OnToolCall: func(tc aap.ToolCall) {
			fmt.Fprintf(os.Stderr, "\n[tool_call %s]\n", tc.Name)
		},
		OnLog: func(l aap.Log) {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", l.Level, l.Message)
		},
		Approve: func(ar aap.ApprovalRequest) aap.ApprovalDecision {
			fmt.Fprintf(os.Stderr, "[approve %s -> allow]\n", ar.ToolName)
			return aap.DecisionAllow
		},
	}
}

func printTurnSummary(tc *aap.TurnComplete) {
	stop := ""
	if tc.StopReason != nil {
		stop = *tc.StopReason
	}
	fmt.Fprintf(os.Stderr, "turn_complete: is_error=%v stop_reason=%s", tc.IsError, stop)
	if tc.Usage != nil {
		fmt.Fprintf(os.Stderr, " tokens(in=%d out=%d) cost=$%.4f",
			tc.Usage.InputTokens, tc.Usage.OutputTokens, tc.Usage.TotalCostUSD)
	}
	fmt.Fprintln(os.Stderr)
}
