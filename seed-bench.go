//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	counts := []int{1000, 10000, 50000, 100000}

	for _, count := range counts {
		dir := fmt.Sprintf("/tmp/tk-bench/%d/.tickets", count)
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
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Use fixed number of workers for I/O parallelism
	const numWorkers = 8
	ticketsChan := make(chan int, numWorkers*2)

	var wg sync.WaitGroup

	// Start workers
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ticketsChan {
				writeTicket(dir, i)
			}
		}()
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

	content := fmt.Sprintf(`---
id: %s
status: %s
%sblocked-by: []
created: 2026-01-04T12:00:00Z
type: task
priority: %d
assignee: Test User
---
# Test ticket %d

Description for ticket %d.
`, id, status, closedLine, priority, i, i)

	os.WriteFile(path, []byte(content), 0644)
}
