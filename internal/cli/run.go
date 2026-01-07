package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

// Run is the main entry point. Returns exit code.
// sigCh can be nil if signal handling is not needed (e.g., in tests).
func Run(_ io.Reader, out io.Writer, errOut io.Writer, args []string, env map[string]string, sigCh <-chan os.Signal) int {
	// Create fresh global flags for this invocation
	globalFlags := flag.NewFlagSet("tk", flag.ContinueOnError)
	globalFlags.SetInterspersed(false)
	globalFlags.Usage = func() {}
	globalFlags.SetOutput(&strings.Builder{})
	flagHelp := globalFlags.BoolP("help", "h", false, "Show help")
	flagCwd := globalFlags.StringP("cwd", "C", "", "Run as if started in `dir`")
	flagConfig := globalFlags.StringP("config", "c", "", "Use specified config `file`")
	flagTicketDir := globalFlags.String("ticket-dir", "", "Override ticket `directory`")

	// Validate global flags.
	if err := globalFlags.Parse(args[1:]); err != nil {
		fprintln(errOut, "error:", err)
		printGlobalOptions(errOut)

		return 1
	}

	if globalFlags.Changed("ticket-dir") && *flagTicketDir == "" {
		fprintln(errOut, "error:", ticket.ErrTicketDirEmpty)
		printGlobalOptions(errOut)

		return 1
	}

	// Ensure that configuration can be loaded an is valid.
	cfg, err := ticket.LoadConfig(ticket.LoadConfigInput{
		WorkDirOverride:   *flagCwd,
		ConfigPath:        *flagConfig,
		TicketDirOverride: *flagTicketDir,
		Env:               env,
	})
	if err != nil {
		fprintln(errOut, "error:", err)
		printGlobalOptions(errOut)

		return 1
	}

	// Create all commands so that from now on, we can show
	// all of them inside error output/help.
	commands := allCommands(cfg, env)

	commandMap := make(map[string]*Command, len(commands))
	for _, cmd := range commands {
		commandMap[cmd.Name()] = cmd
	}

	commandAndArgs := globalFlags.Args()

	// Show help: explicit --help or bare `tk` with no args
	if *flagHelp || (len(commandAndArgs) == 0 && globalFlags.NFlag() == 0) {
		printUsage(out, commands)

		return 0
	}

	// Flags provided but no command: `tk --cwd /tmp`
	if len(commandAndArgs) == 0 {
		fprintln(errOut, "error: no command provided")
		printUsage(errOut, commands)

		return 1
	}

	// Dispatch to command
	cmdName := commandAndArgs[0]

	cmd, ok := commandMap[cmdName]
	if !ok {
		fprintln(errOut, "error: unknown command:", cmdName)
		printUsage(errOut, commands)

		return 1
	}

	cmdIO := NewIO(out, errOut)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run command in goroutine so we can handle signals
	done := make(chan int, 1)

	go func() {
		done <- cmd.Run(ctx, cmdIO, commandAndArgs[1:])
	}()

	// Wait for completion or first signal (nil channel never fires)
	select {
	case exitCode := <-done:
		if exitCode != 0 {
			return exitCode
		}

		return cmdIO.Finish()
	case <-sigCh:
		fprintln(errOut, "shutting down with 5s timeout...")
		cancel()
	}

	// Wait for completion, timeout, or second signal
	select {
	case <-done:
		fprintln(errOut, "graceful shutdown ok (130)")

		return 130
	case <-time.After(5 * time.Second):
		fprintln(errOut, "graceful shutdown timed out, forced exit (130)")

		return 130
	case <-sigCh:
		fprintln(errOut, "graceful shutdown interrupted, forced exit (130)")

		return 130
	}
}

// allCommands returns all commands in display order.
// Dependencies are captured via closures in each command constructor.
func allCommands(cfg ticket.Config, env map[string]string) []*Command {
	return []*Command{
		ShowCmd(cfg),
		CreateCmd(cfg),
		LsCmd(cfg),
		StartCmd(cfg),
		CloseCmd(cfg),
		ReopenCmd(cfg),
		BlockCmd(cfg),
		UnblockCmd(cfg),
		ReadyCmd(cfg),
		RepairCmd(cfg),
		EditorCmd(cfg, env),
		PrintConfigCmd(cfg),
	}
}

func fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

const globalOptionsHelp = `  -h, --help             Show help
  -C, --cwd <dir>        Run as if started in <dir>
  -c, --config <file>    Use specified config file
  --ticket-dir <dir>     Override ticket directory`

func printGlobalOptions(w io.Writer) {
	fprintln(w, "Usage: tk [flags] <command> [args]")
	fprintln(w)
	fprintln(w, "Global flags:")
	fprintln(w, globalOptionsHelp)
	fprintln(w)
	fprintln(w, "Run 'tk --help' for a list of commands.")
}

func printUsage(w io.Writer, commands []*Command) {
	fprintln(w, "tk - minimal ticket system")
	fprintln(w)
	fprintln(w, "Usage: tk [flags] <command> [args]")
	fprintln(w)
	fprintln(w, "Flags:")
	fprintln(w, globalOptionsHelp)
	fprintln(w)
	fprintln(w, "Commands:")

	for _, cmd := range commands {
		fprintln(w, cmd.HelpLine())
	}
}
