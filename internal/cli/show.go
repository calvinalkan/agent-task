package cli

import (
	"context"
	"fmt"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

// ShowCmd returns the show command.
func ShowCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("show", flag.ContinueOnError),
		Usage: "show <id>",
		Short: "Show ticket details",
		Long:  "Display the full contents of a ticket.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execShow(io, cfg, args)
		},
	}
}

func execShow(io *IO, cfg ticket.Config, args []string) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	content, err := ticket.ReadTicket(path)
	if err != nil {
		return err
	}

	io.Printf("%s", content)

	return nil
}
