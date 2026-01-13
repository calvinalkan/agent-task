package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// RepairCmd returns the repair command.
func RepairCmd(cfg *ticket.Config) *Command {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	fs.Bool("all", false, "Repair all tickets")
	fs.Bool("dry-run", false, "Show what would be fixed without writing")
	fs.Bool("rebuild-cache", false, "Rebuild ticket cache from scratch")

	return &Command{
		Flags: fs,
		Usage: "repair [<id> | --all] [flags]",
		Short: "Repair ticket inconsistencies",
		Long: `Fix ticket inconsistencies like stale blocker references.

Use --all to repair all tickets, or specify a single ticket ID.
Use --dry-run to preview changes without writing.`,
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execRepair(io, cfg, fs, args)
		},
	}
}

func execRepair(io *IO, cfg *ticket.Config, fs *flag.FlagSet, args []string) error {
	all, _ := fs.GetBool("all")
	dryRun, _ := fs.GetBool("dry-run")
	rebuildCache, _ := fs.GetBool("rebuild-cache")

	if rebuildCache {
		results, err := ticket.BuildCacheParallelLocked(cfg.TicketDirAbs, nil)
		if err != nil {
			return fmt.Errorf("rebuild cache: %w", err)
		}

		for _, r := range results {
			if r.Err != nil {
				io.WarnLLM(
					fmt.Sprintf("%s: %v", r.Path, r.Err),
					"fix the ticket file or delete it if invalid",
				)
			}
		}

		io.Println("Rebuilt cache")

		return nil
	}

	if !all && len(args) == 0 {
		return ticket.ErrIDRequired
	}

	if all {
		return repairAllTickets(io, cfg.TicketDirAbs, dryRun)
	}

	ticketID := args[0]

	return repairSingleTicket(io, cfg.TicketDirAbs, ticketID, dryRun)
}

func repairSingleTicket(io *IO, ticketDirAbs, ticketID string, dryRun bool) error {
	if !ticket.Exists(ticketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDirAbs, ticketID)

	blockedBy, err := ticket.ReadTicketBlockedBy(path)
	if err != nil {
		return fmt.Errorf("read blocked_by: %w", err)
	}

	staleBlockers := findStaleBlockers(ticketDirAbs, blockedBy)

	if len(staleBlockers) == 0 {
		io.Println("Nothing to repair")

		return nil
	}

	for _, stale := range staleBlockers {
		if dryRun {
			io.Println("Would remove stale blocker:", stale)
		} else {
			io.Println("Removed stale blocker:", stale)
		}
	}

	if dryRun {
		return nil
	}

	newBlockedBy := removeItems(blockedBy, staleBlockers)

	err = ticket.UpdateTicketBlockedByLocked(path, newBlockedBy)
	if err != nil {
		return fmt.Errorf("update blocked_by: %w", err)
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return fmt.Errorf("parse frontmatter: %w", parseErr)
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return fmt.Errorf("update cache: %w", cacheErr)
	}

	io.Println("Repaired", ticketID)

	return nil
}

func repairAllTickets(io *IO, ticketDirAbs string, dryRun bool) error {
	results, err := ticket.ListTickets(ticketDirAbs, &ticket.ListTicketsOptions{Limit: 0}, nil)
	if err != nil {
		return fmt.Errorf("list tickets: %w", err)
	}

	validIDs := buildValidIDMap(io, results)
	anyRepaired := false

	for _, result := range results {
		if result.Err != nil {
			continue
		}

		repaired, repairErr := repairTicketBlockers(io, result.Summary, validIDs, dryRun)
		if repairErr != nil {
			return repairErr
		}

		if repaired {
			anyRepaired = true
		}
	}

	if !anyRepaired {
		io.Println("Nothing to repair")
	}

	return nil
}

func buildValidIDMap(io *IO, results []ticket.Result) map[string]bool {
	validIDs := make(map[string]bool)

	for _, result := range results {
		if result.Err != nil {
			io.WarnLLM(
				fmt.Sprintf("%s: %v", result.Path, result.Err),
				"fix the ticket file or delete it if invalid",
			)

			continue
		}

		validIDs[result.Summary.ID] = true
	}

	return validIDs
}

func repairTicketBlockers(io *IO, summary *ticket.Summary, validIDs map[string]bool, dryRun bool) (bool, error) {
	staleBlockers := findStaleBlockersFromMap(summary.BlockedBy, validIDs)

	if len(staleBlockers) == 0 {
		return false, nil
	}

	for _, stale := range staleBlockers {
		if dryRun {
			io.Println("Would remove stale blocker:", stale)
		} else {
			io.Println("Removed stale blocker:", stale)
		}
	}

	if !dryRun {
		newBlockedBy := removeItems(summary.BlockedBy, staleBlockers)

		updateErr := ticket.UpdateTicketBlockedByLocked(summary.Path, newBlockedBy)
		if updateErr != nil {
			return false, fmt.Errorf("updating blocked-by: %w", updateErr)
		}

		updated := *summary
		updated.BlockedBy = newBlockedBy

		ticketDir := filepath.Dir(summary.Path)
		filename := filepath.Base(summary.Path)

		cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, filename, &updated)
		if cacheErr != nil {
			return false, fmt.Errorf("updating cache: %w", cacheErr)
		}

		io.Println("Repaired", summary.ID)
	}

	return true, nil
}

func findStaleBlockers(ticketDirAbs string, blockedBy []string) []string {
	var stale []string

	for _, blockerID := range blockedBy {
		if !ticket.Exists(ticketDirAbs, blockerID) {
			stale = append(stale, blockerID)
		}
	}

	return stale
}

func findStaleBlockersFromMap(blockedBy []string, validIDs map[string]bool) []string {
	var stale []string

	for _, blockerID := range blockedBy {
		if !validIDs[blockerID] {
			stale = append(stale, blockerID)
		}
	}

	return stale
}

func removeItems(slice, toRemove []string) []string {
	result := make([]string, 0, len(slice))

	for _, item := range slice {
		if !slices.Contains(toRemove, item) {
			result = append(result, item)
		}
	}

	return result
}
