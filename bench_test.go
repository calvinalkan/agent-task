package main

import (
	"bytes"
	"testing"
)

func BenchmarkLs100k(b *testing.B) {
	cfg := Config{TicketDir: ".tickets"}
	workDir := "/tmp/tk-bench/100000"
	var out, errOut bytes.Buffer

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		errOut.Reset()
		cmdLs(&out, &errOut, cfg, workDir, []string{"--limit=10"})
	}
}

func BenchmarkReady100k(b *testing.B) {
	cfg := Config{TicketDir: ".tickets"}
	workDir := "/tmp/tk-bench/100000"
	var out, errOut bytes.Buffer

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		errOut.Reset()
		cmdReady(&out, &errOut, cfg, workDir, []string{})
	}
}
