package main

import (
	"io"
	"path/filepath"
	"slices"

	flag "github.com/spf13/pflag"
)

const repairHelp = `  repair <id>            Repair ticket inconsistencies`

func cmdRepair(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printRepairHelp(out)

		return 0
	}

	flagSet := flag.NewFlagSet("repair", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	allFlag := flagSet.Bool("all", false, "Repair all tickets")
	dryRun := flagSet.Bool("dry-run", false, "Show what would be fixed without writing")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	remaining := flagSet.Args()

	// Validate: need either --all or an ID
	if !*allFlag && len(remaining) == 0 {
		fprintln(errOut, "error:", errIDRequired)

		return 1
	}

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	if *allFlag {
		return repairAllTickets(out, errOut, ticketDir, *dryRun)
	}

	ticketID := remaining[0]

	return repairSingleTicket(out, errOut, ticketDir, ticketID, *dryRun)
}

func repairSingleTicket(out io.Writer, errOut io.Writer, ticketDir, ticketID string, dryRun bool) int {
	// Check if ticket exists
	if !TicketExists(ticketDir, ticketID) {
		fprintln(errOut, "error:", errTicketNotFound, ticketID)

		return 1
	}

	path := TicketPath(ticketDir, ticketID)

	// Read current blocked-by list
	blockedBy, err := ReadTicketBlockedBy(path)
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

	err = UpdateTicketBlockedBy(path, newBlockedBy)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, "Repaired", ticketID)

	return 0
}

func repairAllTickets(out io.Writer, errOut io.Writer, ticketDir string, dryRun bool) int {
	results, err := ListTickets(ticketDir, ListTicketsOptions{NeedAll: true})
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

func buildValidIDMap(results []TicketResult, errOut io.Writer) map[string]bool {
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

func repairTicketBlockers(out io.Writer, summary *TicketSummary, validIDs map[string]bool, dryRun bool) (bool, error) {
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

		updateErr := UpdateTicketBlockedBy(summary.Path, newBlockedBy)
		if updateErr != nil {
			return false, updateErr
		}

		fprintln(out, "Repaired", summary.ID)
	}

	return true, nil
}

func findStaleBlockers(ticketDir string, blockedBy []string) []string {
	var stale []string

	for _, blockerID := range blockedBy {
		if !TicketExists(ticketDir, blockerID) {
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
