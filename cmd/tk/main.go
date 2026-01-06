// Package main provides tk, a minimal ticket system with dependency tracking.
package main

import (
	"os"
	"os/signal"
	"strings"
	"syscall"

	"tk/internal/cli"
)

func main() {
	environ := os.Environ()
	env := make(map[string]string, len(environ))

	for _, e := range environ {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	exitCode := cli.Run(os.Stdin, os.Stdout, os.Stderr, os.Args, env, sigCh)

	os.Exit(exitCode)
}
