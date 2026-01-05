// Package main provides tk-seed, a tool to seed test tickets.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

func main() {
	counts := []int{1000, 500000}
	baseDir := filepath.Join(os.TempDir(), "tk-bench")

	for _, count := range counts {
		dir := filepath.Join(baseDir, strconv.Itoa(count), ".tickets")
		start := time.Now()

		err := seedTickets(dir, count)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error seeding %d: %v\n", count, err)
			os.Exit(1)
		}

		fmt.Printf("Created %d tickets in %s -> %s\n", count, time.Since(start), dir)
	}
}

func seedTickets(dir string, count int) error {
	// Remove and recreate directory
	_ = os.RemoveAll(dir)

	err := os.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Use number of CPU cores for I/O parallelism
	numWorkers := runtime.NumCPU()
	ticketsChan := make(chan int, numWorkers*2)

	var wg sync.WaitGroup

	// Start workers
	for range numWorkers {
		wg.Go(func() {
			for i := range ticketsChan {
				writeTicket(dir, i)
			}
		})
	}

	// Send work
	for i := 1; i <= count; i++ {
		ticketsChan <- i
	}

	close(ticketsChan)

	wg.Wait()

	return nil
}

func writeTicket(dir string, i int) {
	id := fmt.Sprintf("t%06d", i)
	path := filepath.Join(dir, id+".md")

	// Vary status for realistic distribution
	status := "open"
	closedLine := ""

	if i%5 == 0 {
		status = "closed"
		closedLine = "closed: 2026-01-04T13:00:00Z\n"
	} else if i%7 == 0 {
		status = "in_progress"
	}

	// Vary priority
	priority := (i % 4) + 1

	// Vary type for realistic distribution
	types := []string{"bug", "feature", "task", "epic", "chore"}
	ticketType := types[i%len(types)]

	content := fmt.Sprintf(`---
schema_version: 1
id: %s
status: %s
%sblocked-by: []
created: 2026-01-04T12:00:00Z
type: %s
priority: %d
assignee: Test User
---
# Test ticket %d

Description for ticket %d.
`, id, status, closedLine, ticketType, priority, i, i)

	_ = os.WriteFile(path, []byte(content), 0o600)
}
