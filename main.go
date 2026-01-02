// Package main provides tk, a minimal ticket system with dependency tracking.
package main

import "os"

func main() {
	os.Exit(Run(os.Stdin, os.Stdout, os.Stderr, os.Args, os.Environ()))
}
