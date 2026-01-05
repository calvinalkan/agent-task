package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

const repairHelp = `  repair <id>            Repair ticket inconsistencies`

func cmdRepair(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printRepairHelp(out)

		return 0
	}

	flagSet := flag.NewFlagSet("repair", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	allFlag := flagSet.Bool("all", false, "Repair all tickets")
	dryRun := flagSet.Bool("dry-run", false, "Show what would be fixed without writing")
	rebuildCache := flagSet.Bool("rebuild-cache", false, "Rebuild ticket cache from scratch")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	remaining := flagSet.Args()

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	if *rebuildCache {
		results, err := ticket.BuildCacheParallelLocked(ticketDir, nil)
		if err != nil {
			fprintln(errOut, "error:", err)

			return 1
		}

		hasWarnings := false

		for _, r := range results {
			if r.Err != nil {
				fprintln(errOut, "warning:", r.Path+":", r.Err)

				hasWarnings = true
			}
		}

		fprintln(out, "Rebuilt cache")

		if hasWarnings {
			return 1
		}

		return 0
	}

	// Validate: need either --all or an ID
	if !*allFlag && len(remaining) == 0 {
		fprintln(errOut, "error:", ticket.ErrIDRequired)

		return 1
	}

	if *allFlag {
		return repairAllTickets(out, errOut, ticketDir, *dryRun)
	}

	ticketID := remaining[0]

	return repairSingleTicket(out, errOut, ticketDir, ticketID, *dryRun)
}

func repairSingleTicket(out io.Writer, errOut io.Writer, ticketDir, ticketID string, dryRun bool) int {
	// Check if ticket exists
	if !ticket.Exists(ticketDir, ticketID) {
		fprintln(errOut, "error:", ticket.ErrTicketNotFound, ticketID)

		return 1
	}

	path := ticket.Path(ticketDir, ticketID)

	// Read current blocked-by list
	blockedBy, err := ticket.ReadTicketBlockedBy(path)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Find stale blockers
	staleBlockers := findStaleBlockers(ticketDir, blockedBy)

	if len(staleBlockers) == 0 {
		fprintln(out, "Nothing to repair")

		return 0
	}

	// Report what will be/was removed
	for _, stale := range staleBlockers {
		if dryRun {
			fprintln(out, "Would remove stale blocker:", stale)
		} else {
			fprintln(out, "Removed stale blocker:", stale)
		}
	}

	if dryRun {
		return 0
	}

	// Remove stale blockers
	newBlockedBy := removeItems(blockedBy, staleBlockers)

	err = ticket.UpdateTicketBlockedByLocked(path, newBlockedBy)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		fprintln(errOut, "error:", cacheErr)

		return 1
	}

	fprintln(out, "Repaired", ticketID)

	return 0
}

func repairAllTickets(out io.Writer, errOut io.Writer, ticketDir string, dryRun bool) int {
	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, errOut)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	validIDs := buildValidIDMap(results, errOut)
	anyRepaired := false

	for _, result := range results {
		if result.Err != nil {
			continue
		}

		repaired, repairErr := repairTicketBlockers(out, result.Summary, validIDs, dryRun)
		if repairErr != nil {
			fprintln(errOut, "error:", repairErr)

			return 1
		}

		if repaired {
			anyRepaired = true
		}
	}

	if !anyRepaired {
		fprintln(out, "Nothing to repair")
	}

	return 0
}

func buildValidIDMap(results []ticket.Result, errOut io.Writer) map[string]bool {
	validIDs := make(map[string]bool)

	for _, result := range results {
		if result.Err != nil {
			fprintln(errOut, "warning:", result.Path+":", result.Err)

			continue
		}

		validIDs[result.Summary.ID] = true
	}

	return validIDs
}

func repairTicketBlockers(out io.Writer, summary *ticket.Summary, validIDs map[string]bool, dryRun bool) (bool, error) {
	staleBlockers := findStaleBlockersFromMap(summary.BlockedBy, validIDs)

	if len(staleBlockers) == 0 {
		return false, nil
	}

	// Report what will be/was removed
	for _, stale := range staleBlockers {
		if dryRun {
			fprintln(out, "Would remove stale blocker:", stale)
		} else {
			fprintln(out, "Removed stale blocker:", stale)
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

		fprintln(out, "Repaired", summary.ID)
	}

	return true, nil
}

func findStaleBlockers(ticketDir string, blockedBy []string) []string {
	var stale []string

	for _, blockerID := range blockedBy {
		if !ticket.Exists(ticketDir, blockerID) {
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

func printRepairHelp(out io.Writer) {
	fprintln(out, "Usage: tk repair <id>")
	fprintln(out, "       tk repair --all")
	fprintln(out, "")
	fprintln(out, "Fix ticket inconsistencies like stale blocker references.")
	fprintln(out, "")
	fprintln(out, "Options:")
	fprintln(out, "  --all      Repair all tickets instead of single ID")
	fprintln(out, "  --dry-run  Show what would be fixed without writing")
}
