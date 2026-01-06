package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

const repairHelp = `  repair <id>            Repair ticket inconsistencies`

func cmdRepair(io *IO, cfg ticket.Config, workDir string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printRepairHelp(io)

		return nil
	}

	flagSet := flag.NewFlagSet("repair", flag.ContinueOnError)
	flagSet.SetOutput(&strings.Builder{}) // discard

	allFlag := flagSet.Bool("all", false, "Repair all tickets")
	dryRun := flagSet.Bool("dry-run", false, "Show what would be fixed without writing")
	rebuildCache := flagSet.Bool("rebuild-cache", false, "Rebuild ticket cache from scratch")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		return parseErr
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
			return err
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

	// Validate: need either --all or an ID
	if !*allFlag && len(remaining) == 0 {
		return ticket.ErrIDRequired
	}

	if *allFlag {
		return repairAllTickets(io, ticketDir, *dryRun)
	}

	ticketID := remaining[0]

	return repairSingleTicket(io, ticketDir, ticketID, *dryRun)
}

func repairSingleTicket(io *IO, ticketDir, ticketID string, dryRun bool) error {
	// Check if ticket exists
	if !ticket.Exists(ticketDir, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDir, ticketID)

	// Read current blocked-by list
	blockedBy, err := ticket.ReadTicketBlockedBy(path)
	if err != nil {
		return err
	}

	// Find stale blockers
	staleBlockers := findStaleBlockers(ticketDir, blockedBy)

	if len(staleBlockers) == 0 {
		io.Println("Nothing to repair")

		return nil
	}

	// Report what will be/was removed
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

	// Remove stale blockers
	newBlockedBy := removeItems(blockedBy, staleBlockers)

	err = ticket.UpdateTicketBlockedByLocked(path, newBlockedBy)
	if err != nil {
		return err
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return parseErr
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	io.Println("Repaired", ticketID)

	return nil
}

func repairAllTickets(io *IO, ticketDir string, dryRun bool) error {
	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, nil)
	if err != nil {
		return err
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

	// Report what will be/was removed
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

func printRepairHelp(io *IO) {
	io.Println("Usage: tk repair <id>")
	io.Println("       tk repair --all")
	io.Println("")
	io.Println("Fix ticket inconsistencies like stale blocker references.")
	io.Println("")
	io.Println("Options:")
	io.Println("  --all      Repair all tickets instead of single ID")
	io.Println("  --dry-run  Show what would be fixed without writing")
}
