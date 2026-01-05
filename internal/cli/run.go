package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"tk/internal/ticket"
)

const (
	minArgs      = 2
	consumedOne  = 1
	consumedTwo  = 2
	consumedNone = 0
	helpFlag     = "--help"
)

// Run is the main entry point. Returns exit code.
func Run(_ io.Reader, out io.Writer, errOut io.Writer, args []string, env map[string]string) int {
	if len(args) < minArgs {
		printUsage(out)

		return 0
	}

	// Parse global flags
	flags, err := parseGlobalFlags(args[1:])
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Default workDir to current directory
	workDir := flags.workDir
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			fprintln(errOut, "error: cannot get working directory:", err)

			return 1
		}
	}

	// Load and validate config
	cliOverrides := ticket.Config{TicketDir: flags.ticketDir}

	cfg, sources, err := ticket.LoadConfig(workDir, flags.configPath, cliOverrides, flags.hasTicketDirOverride, env)
	if err != nil {
		fprintln(errOut, "error:", err)
		printUsage(errOut)

		return 1
	}

	if len(flags.remaining) == 0 {
		printUsage(out)

		return 0
	}

	cmd := flags.remaining[0]
	_ = flags.remaining[1:] // remaining args for command

	// Handle help flags
	if cmd == "-h" || cmd == helpFlag {
		printUsage(out)

		return 0
	}

	switch cmd {
	case "create":
		return cmdCreate(out, errOut, cfg, workDir, flags.remaining[1:])
	case "show":
		return cmdShow(out, errOut, cfg, workDir, flags.remaining[1:])
	case "ls":
		return cmdLs(out, errOut, cfg, workDir, flags.remaining[1:])
	case "start":
		return cmdStart(out, errOut, cfg, workDir, flags.remaining[1:])
	case "close":
		return cmdClose(out, errOut, cfg, workDir, flags.remaining[1:])
	case "reopen":
		return cmdReopen(out, errOut, cfg, workDir, flags.remaining[1:])
	case "block":
		return cmdBlock(out, errOut, cfg, workDir, flags.remaining[1:])
	case "unblock":
		return cmdUnblock(out, errOut, cfg, workDir, flags.remaining[1:])
	case "ready":
		return cmdReady(out, errOut, cfg, workDir, flags.remaining[1:])
	case "repair":
		return cmdRepair(out, errOut, cfg, workDir, flags.remaining[1:])
	case "editor":
		return cmdEditor(out, errOut, cfg, workDir, flags.remaining[1:], env)
	case "print-config":
		return cmdPrintConfig(out, errOut, cfg, sources)
	default:
		fprintln(errOut, "error: unknown command:", cmd)
		printUsage(errOut)

		return 1
	}
}

type globalFlags struct {
	workDir              string
	configPath           string
	ticketDir            string
	hasTicketDirOverride bool
	remaining            []string
}

func parseGlobalFlags(args []string) (globalFlags, error) {
	var flags globalFlags

	idx := 0
	for idx < len(args) {
		consumed, err := parseFlag(args, idx, &flags)
		if err != nil {
			return globalFlags{}, err
		}

		if consumed == 0 {
			// Not a flag, this is the command
			flags.remaining = args[idx:]

			break
		}

		idx += consumed
	}

	return flags, nil
}

// parseFlag tries to parse a flag at args[idx]. Returns number of args consumed (0 if not a flag).
func parseFlag(args []string, idx int, flags *globalFlags) (int, error) {
	arg := args[idx]

	// -C/--cwd flag (work directory)
	if (arg == "-C" || arg == "--cwd") && idx+1 < len(args) {
		flags.workDir = args[idx+1]

		return consumedTwo, nil
	}

	if after, ok := strings.CutPrefix(arg, "-C"); ok {
		flags.workDir = after

		return consumedOne, nil
	}

	if after, ok := strings.CutPrefix(arg, "--cwd="); ok {
		flags.workDir = after

		return consumedOne, nil
	}

	// -c/--config flag
	if arg == "-c" || arg == "--config" {
		if idx+1 >= len(args) {
			return consumedNone, fmt.Errorf("%w: %s", ticket.ErrFlagRequiresArg, arg)
		}

		flags.configPath = args[idx+1]

		return consumedTwo, nil
	}

	if after, ok := strings.CutPrefix(arg, "--config="); ok {
		flags.configPath = after

		return consumedOne, nil
	}

	// --ticket-dir flag
	if arg == "--ticket-dir" {
		if idx+1 >= len(args) {
			return consumedNone, fmt.Errorf("%w: %s", ticket.ErrFlagRequiresArg, arg)
		}

		flags.ticketDir = args[idx+1]
		flags.hasTicketDirOverride = true

		return consumedTwo, nil
	}

	if after, ok := strings.CutPrefix(arg, "--ticket-dir="); ok {
		flags.ticketDir = after
		flags.hasTicketDirOverride = true

		return consumedOne, nil
	}

	// -h/--help flags
	if arg == "-h" || arg == helpFlag {
		flags.remaining = []string{helpFlag}

		return len(args) - idx, nil
	}

	// Unknown flag
	if strings.HasPrefix(arg, "-") && arg != "-" {
		return consumedNone, fmt.Errorf("%w: %s", ticket.ErrUnknownFlag, arg)
	}

	// Not a flag
	return consumedNone, nil
}

func cmdPrintConfig(out io.Writer, errOut io.Writer, cfg ticket.Config, sources ticket.ConfigSources) int {
	formatted, err := ticket.FormatConfig(cfg)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, formatted)

	// Print sources
	fprintln(out, "")
	fprintln(out, "# Sources:")

	if sources.Global != "" {
		fprintln(out, "#   global:", sources.Global)
	}

	if sources.Project != "" {
		fprintln(out, "#   project:", sources.Project)
	}

	if sources.Global == "" && sources.Project == "" {
		fprintln(out, "#   (using defaults only)")
	}

	return 0
}

func fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == helpFlag {
			return true
		}
	}

	return false
}

func printUsage(writer io.Writer) {
	fprintln(writer, `tk - minimal ticket system

Usage: tk [options] <command> [args]

Options:
  -C, --cwd <dir>    Run as if started in <dir>
  -c, --config       Use specified config file

Commands:`)
	fprintln(writer, createHelp)
	fprintln(writer, showHelp)
	fprintln(writer, `  ls [--status=X]        List tickets`)
	fprintln(writer, startHelp)
	fprintln(writer, closeHelp)
	fprintln(writer, reopenHelp)
	fprintln(writer, blockHelp)
	fprintln(writer, unblockHelp)
	fprintln(writer, readyHelp)
	fprintln(writer, repairHelp)
	fprintln(writer, editorHelp)
	fprintln(writer, `  print-config           Show resolved configuration`)
}
