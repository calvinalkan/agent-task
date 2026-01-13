package cli

import (
	"context"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// PrintConfigCmd returns the print-config command.
func PrintConfigCmd(cfg *ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("print-config", flag.ContinueOnError),
		Usage: "print-config",
		Short: "Show resolved configuration",
		Long:  "Display the effective configuration and which files it was loaded from.",
		Exec: func(_ context.Context, io *IO, _ []string) error {
			return execPrintConfig(io, cfg)
		},
	}
}

func execPrintConfig(io *IO, cfg *ticket.Config) error {
	io.Println("effective_cwd=" + cfg.EffectiveCwd)
	io.Println("ticket_dir=" + cfg.TicketDirAbs)

	if cfg.Editor != "" {
		io.Println("editor=" + cfg.Editor)
	}

	io.Println("")
	io.Println("# sources")

	if cfg.Sources.Global == "" && cfg.Sources.Project == "" {
		io.Println("(defaults only)")
	} else {
		if cfg.Sources.Global != "" {
			io.Println("global_config=" + cfg.Sources.Global)
		}

		if cfg.Sources.Project != "" {
			io.Println("project_config=" + cfg.Sources.Project)
		}
	}

	return nil
}
