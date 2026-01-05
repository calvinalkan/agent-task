// Package main provides tk, a minimal ticket system with dependency tracking.
package main

import (
	"os"
	"strings"

	"tk/internal/cli"
)

func main() {
	env := make(map[string]string)

	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}

	os.Exit(cli.Run(os.Stdin, os.Stdout, os.Stderr, os.Args, env))
}
