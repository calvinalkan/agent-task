package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	flag "github.com/spf13/pflag"
)

// Command defines a CLI command with unified help generation.
type Command struct {
	// Flags defines command-specific flags.
	// The FlagSet name is not used - command identity comes from Usage.
	Flags *flag.FlagSet

	// Usage is the freeform usage string shown after "tk" in help.
	// Includes the command name and arguments/flags.
	// Examples: "show <id>", "create -t <title> [flags]", "ls [flags]"
	Usage string

	// Short is a one-line description for the global help listing.
	Short string

	// Long is the full description shown in command help.
	// If empty, Short is used instead.
	Long string

	// Exec runs the command after flags are parsed.
	Exec func(ctx context.Context, o *IO, args []string) error
}

// Name returns the command name (first word of Usage).
func (c *Command) Name() string {
	name, _, _ := strings.Cut(c.Usage, " ")
	return name
}

// HelpLine returns the short help line for the main usage display.
func (c *Command) HelpLine() string {
	return fmt.Sprintf("  %-22s %s", c.Usage, c.Short)
}

// PrintHelp prints the full help output for "tk <cmd> --help".
func (c *Command) PrintHelp(o *IO) {
	o.Println("Usage: tk", c.Usage)
	o.Println()

	desc := c.Long
	if desc == "" {
		desc = c.Short
	}

	o.Println(desc)

	if c.Flags != nil && c.Flags.HasFlags() {
		o.Println()
		o.Println("Flags:")

		var buf strings.Builder
		c.Flags.SetOutput(&buf)
		c.Flags.PrintDefaults()
		o.Printf("%s", buf.String())
	}
}

// Run parses flags and executes the command. Returns exit code.
// Handles error printing internally for consistent output ordering.
func (c *Command) Run(ctx context.Context, o *IO, args []string) int {
	c.Flags.SetOutput(&strings.Builder{}) // discard pflag output

	err := c.Flags.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.PrintHelp(o)
			return 0
		}
		o.ErrPrintln("error:", err)
		o.ErrPrintln()
		c.PrintHelp(o)
		return 1
	}

	if err := c.Exec(ctx, o, c.Flags.Args()); err != nil {
		o.ErrPrintln("error:", err)
		return 1
	}

	return 0
}
