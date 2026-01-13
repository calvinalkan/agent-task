// Package main provides tk-bench, a benchmark tool for tk.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	errHyperfineNotFound = errors.New("hyperfine not found; install it first")
	errNoHyperfineResult = errors.New("no results in hyperfine output")
)

// Config holds all benchmark configuration.
type Config struct {
	Bin       string
	BenchRoot string
	Counts    []int
	OutDir    string
	Warmup    int
	MinRuns   int
	MaxRuns   int

	// Cache stress specific
	WarmRuns           int
	ColdRunsSmall      int
	ColdRunsMed        int
	ColdRunsLarge      int
	ChurnRunsSmall     int
	ChurnRunsMed       int
	ChurnRunsLarge     int
	ReconcileRunsSmall int
	ReconcileRunsMed   int
	ReconcileRunsLarge int
}

// HyperfineResultEntry represents a single hyperfine benchmark result.
type HyperfineResultEntry struct {
	Command string    `json:"command"`
	Mean    float64   `json:"mean"`
	Stddev  float64   `json:"stddev"`
	Median  float64   `json:"median"`
	User    float64   `json:"user"`
	System  float64   `json:"system"`
	Min     float64   `json:"min"`
	Max     float64   `json:"max"`
	Times   []float64 `json:"times"`
}

// HyperfineResult represents hyperfine JSON output.
type HyperfineResult struct {
	Results []HyperfineResultEntry `json:"results"`
}

// BenchResult holds a single benchmark result.
type BenchResult struct {
	Label string
	Runs  int
	Mean  float64
	Min   float64
	Max   float64
}

func main() {
	cfg := Config{}

	// Find root directory (parent of bench/)
	exe, _ := os.Executable()
	rootDir := filepath.Dir(filepath.Dir(exe))
	// If running with go run, use working directory
	wd, wdErr := os.Getwd()
	if wdErr == nil {
		_, statErr := os.Stat(filepath.Join(wd, "bench"))
		if statErr == nil {
			rootDir = wd
		} else if filepath.Base(wd) == "bench" {
			rootDir = filepath.Dir(wd)
		}
	}

	// Define flags
	flag.StringVar(&cfg.Bin, "bin", filepath.Join(rootDir, "tk"), "Path to tk binary")
	flag.StringVar(&cfg.BenchRoot, "root", "/tmp/tk-bench", "Benchmark data root directory")
	flag.StringVar(&cfg.OutDir, "out", filepath.Join(rootDir, ".benchmarks"), "Output directory for reports")

	countsStr := flag.String("counts", "1000,500000", "Comma-separated list of ticket counts to benchmark")

	flag.IntVar(&cfg.Warmup, "warmup", 3, "Number of warmup runs")
	flag.IntVar(&cfg.MinRuns, "min-runs", 20, "Minimum number of filter benchmark runs")
	flag.IntVar(&cfg.MaxRuns, "max-runs", 0, "Maximum number of filter benchmark runs, 0=unlimited")

	flag.IntVar(&cfg.WarmRuns, "warm-runs", 50, "Warm cache runs for cache stress tests")
	flag.IntVar(&cfg.ColdRunsSmall, "cold-runs-small", 10, "Cold runs for small datasets ≤10k")
	flag.IntVar(&cfg.ColdRunsMed, "cold-runs-med", 5, "Cold runs for medium datasets ≤100k")
	flag.IntVar(&cfg.ColdRunsLarge, "cold-runs-large", 3, "Cold runs for large datasets >100k")
	flag.IntVar(&cfg.ChurnRunsSmall, "churn-runs-small", 10, "Churn runs for small datasets")
	flag.IntVar(&cfg.ChurnRunsMed, "churn-runs-med", 5, "Churn runs for medium datasets")
	flag.IntVar(&cfg.ChurnRunsLarge, "churn-runs-large", 3, "Churn runs for large datasets")
	flag.IntVar(&cfg.ReconcileRunsSmall, "reconcile-runs-small", 10, "Reconcile runs for small datasets")
	flag.IntVar(&cfg.ReconcileRunsMed, "reconcile-runs-med", 5, "Reconcile runs for medium datasets")
	flag.IntVar(&cfg.ReconcileRunsLarge, "reconcile-runs-large", 3, "Reconcile runs for large datasets")

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: go run bench.go [flags]\n\n")
		fmt.Fprint(os.Stderr, "Benchmarks tk performance: ls filters, mutations (start/close/reopen), and cache stress.\n\n")
		fmt.Fprint(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nExamples:\n")
		fmt.Fprint(os.Stderr, "  go run bench.go                           # Run benchmarks with defaults\n")
		fmt.Fprint(os.Stderr, "  go run bench.go -counts=1000              # Quick test with small dataset\n")
		fmt.Fprint(os.Stderr, "  go run bench.go -min-runs=50 -warmup=5    # Custom run counts\n")
	}

	flag.Parse()

	// Parse counts
	for countStr := range strings.SplitSeq(*countsStr, ",") {
		countStr = strings.TrimSpace(countStr)
		if countStr == "" {
			continue
		}

		count, err := strconv.Atoi(countStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid count %q: %v\n", countStr, err)
			os.Exit(1)
		}

		cfg.Counts = append(cfg.Counts, count)
	}

	if len(cfg.Counts) == 0 {
		fmt.Fprint(os.Stderr, "no counts specified\n")
		os.Exit(1)
	}

	// Validate prerequisites
	err := validatePrereqs(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// Create output directory
	err = os.MkdirAll(cfg.OutDir, 0o750)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output directory: %v\n", err)
		os.Exit(1)
	}

	// Run benchmarks
	err = runHyperfineBench(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hyperfine benchmark failed: %v\n", err)
		os.Exit(1)
	}

	err = runMutationBench(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutation benchmark failed: %v\n", err)
		os.Exit(1)
	}

	err = runCacheStressBench(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cache-stress benchmark failed: %v\n", err)
		os.Exit(1)
	}
}

func validatePrereqs(cfg *Config) error {
	// Check hyperfine
	_, err := exec.LookPath("hyperfine")
	if err != nil {
		return errHyperfineNotFound
	}

	// Check tk binary
	info, err := os.Stat(cfg.Bin)
	if err != nil {
		return fmt.Errorf("tk binary not found at %s, run 'make build' or set -bin flag: %w", cfg.Bin, err)
	}

	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("tk binary at %s is not executable: %w", cfg.Bin, os.ErrPermission)
	}

	// Check bench root
	_, err = os.Stat(cfg.BenchRoot)
	if err != nil {
		return fmt.Errorf("bench root missing at %s; run 'go run bench/seed-bench.go' to generate it: %w", cfg.BenchRoot, err)
	}

	return nil
}

func getSystemInfo() string {
	var sb strings.Builder

	timestampUTC := time.Now().UTC().Format(time.RFC3339)
	sb.WriteString(fmt.Sprintf("## Run %s\n\n", timestampUTC))

	ctx := context.Background()

	// Git revision
	gitRev, gitErr := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if gitErr == nil {
		sb.WriteString(fmt.Sprintf("- git: %s\n", strings.TrimSpace(string(gitRev))))
	} else {
		sb.WriteString("- git: unknown\n")
	}

	// Go version
	goVer, goErr := exec.CommandContext(ctx, "go", "version").Output()
	if goErr == nil {
		sb.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(string(goVer))))
	}

	// Hyperfine version
	hfVer, hfErr := exec.CommandContext(ctx, "hyperfine", "--version").Output()
	if hfErr == nil {
		sb.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(string(hfVer))))
	}

	// System info
	sb.WriteString(fmt.Sprintf("- %s/%s\n", runtime.GOOS, runtime.GOARCH))

	sb.WriteString("- note: hyperfine -N (no shell)\n\n")

	return sb.String()
}

func runHyperfineBench(cfg *Config) error {
	timestamp := time.Now().UTC().Format("20060102-150405")
	outFile := filepath.Join(cfg.OutDir, fmt.Sprintf("ls_hyperfine_%s.md", timestamp))

	var report strings.Builder
	report.WriteString(getSystemInfo())

	for _, count := range cfg.Counts {
		workDir := filepath.Join(cfg.BenchRoot, strconv.Itoa(count))
		ticketDir := filepath.Join(workDir, ".tickets")

		_, statErr := os.Stat(ticketDir)
		if statErr != nil {
			report.WriteString(fmt.Sprintf("### Dataset: %d tickets\n\nskipping (missing %s)\n\n", count, ticketDir))

			continue
		}

		fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
		fmt.Fprintf(os.Stderr, "FILTER BENCHMARKS: %d tickets\n", count)
		fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

		// Reset cache
		_ = os.Remove(filepath.Join(ticketDir, ".cache"))
		_ = os.Remove(filepath.Join(ticketDir, ".cache.lock"))

		// Prime cache
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "ls", "--limit=1").Run()

		// Build hyperfine command
		tmpFile, err := os.CreateTemp("", "hyperfine-*.md")
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}

		tmpFileName := tmpFile.Name()
		_ = tmpFile.Close()

		commands := []string{
			fmt.Sprintf("%s -C %s ls", cfg.Bin, workDir),
			fmt.Sprintf("%s -C %s ls --limit=1000", cfg.Bin, workDir),
			fmt.Sprintf("%s -C %s ls --status=open", cfg.Bin, workDir),
			fmt.Sprintf("%s -C %s ls --status=open --priority=1 --type=bug", cfg.Bin, workDir),
		}

		args := []string{"-N", "--warmup", strconv.Itoa(cfg.Warmup), "--min-runs", strconv.Itoa(cfg.MinRuns)}
		if cfg.MaxRuns > 0 {
			args = append(args, "--max-runs", strconv.Itoa(cfg.MaxRuns))
		}

		args = append(args, "--export-markdown", tmpFileName)
		args = append(args, commands...)

		cmd := exec.CommandContext(context.Background(), "hyperfine", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		runErr := cmd.Run()
		if runErr != nil {
			_ = os.Remove(tmpFileName)

			return fmt.Errorf("hyperfine failed for count %d: %w", count, runErr)
		}

		// Read markdown output
		mdContent, err := os.ReadFile(tmpFileName)
		_ = os.Remove(tmpFileName) // cleanup temp file

		if err != nil {
			return fmt.Errorf("failed to read hyperfine output: %w", err)
		}

		report.WriteString(fmt.Sprintf("### Dataset: %d tickets\n\n", count))
		report.WriteString(fmt.Sprintf("- workdir: %s\n", workDir))
		report.WriteString(fmt.Sprintf("- warmup: %d\n", cfg.Warmup))
		report.WriteString(fmt.Sprintf("- min-runs: %d\n", cfg.MinRuns))

		if cfg.MaxRuns > 0 {
			report.WriteString(fmt.Sprintf("- max-runs: %d\n", cfg.MaxRuns))
		}

		report.WriteString("\n")
		report.Write(mdContent)
		report.WriteString("\n")
	}

	err := os.WriteFile(outFile, []byte(report.String()), 0o600)
	if err != nil {
		return fmt.Errorf("failed to write report: %w", err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", outFile)

	return nil
}

func runMutationBench(cfg *Config) error {
	timestamp := time.Now().UTC().Format("20060102-150405")
	outFile := filepath.Join(cfg.OutDir, fmt.Sprintf("mutation_%s.md", timestamp))

	var report strings.Builder
	report.WriteString(getSystemInfo())

	for _, count := range cfg.Counts {
		workDir := filepath.Join(cfg.BenchRoot, strconv.Itoa(count))
		ticketDir := filepath.Join(workDir, ".tickets")

		_, statErr := os.Stat(ticketDir)
		if statErr != nil {
			report.WriteString(fmt.Sprintf("### Dataset: %d tickets\n\nskipping (missing %s)\n\n", count, ticketDir))

			continue
		}

		fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
		fmt.Fprintf(os.Stderr, "MUTATION BENCHMARKS: %d tickets\n", count)
		fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

		// Reset t000001 to open state for consistent starting point
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "reopen", "t000001").Run()

		// Build hyperfine command with prepare steps for each mutation
		tmpFile, err := os.CreateTemp("", "hyperfine-*.md")
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}

		tmpFileName := tmpFile.Name()
		_ = tmpFile.Close()

		// Each command needs a prepare step to reset state:
		// - start: prepare with reopen (ensure ticket is open)
		// - close: prepare with start (ensure ticket is in_progress)
		// - reopen: prepare with close (ensure ticket is closed)
		// State machine: open -> start -> in_progress -> close -> closed -> reopen -> open
		// Prepare commands need to reset to the correct state for each benchmark
		bin := cfg.Bin
		wd := workDir

		type benchCmd struct {
			prepare string
			cmd     string
		}

		commands := []benchCmd{
			{
				// Reset to open: close (if in_progress) then reopen (if closed)
				prepare: fmt.Sprintf("bash -c '%s -C %s close t000001 2>/dev/null; %s -C %s reopen t000001 2>/dev/null; true'", bin, wd, bin, wd),
				cmd:     fmt.Sprintf("%s -C %s start t000001", bin, wd),
			},
			{
				// Reset to in_progress: ensure started
				prepare: fmt.Sprintf("bash -c '%s -C %s close t000001 2>/dev/null; %s -C %s reopen t000001 2>/dev/null; %s -C %s start t000001 2>/dev/null; true'", bin, wd, bin, wd, bin, wd),
				cmd:     fmt.Sprintf("%s -C %s close t000001", bin, wd),
			},
			{
				// Reset to closed: ensure closed
				prepare: fmt.Sprintf("bash -c '%s -C %s start t000001 2>/dev/null; %s -C %s close t000001 2>/dev/null; true'", bin, wd, bin, wd),
				cmd:     fmt.Sprintf("%s -C %s reopen t000001", bin, wd),
			},
		}

		args := []string{"-N", "--warmup", strconv.Itoa(cfg.Warmup), "--min-runs", strconv.Itoa(cfg.MinRuns)}
		if cfg.MaxRuns > 0 {
			args = append(args, "--max-runs", strconv.Itoa(cfg.MaxRuns))
		}

		args = append(args, "--export-markdown", tmpFileName)

		// Add each command with its prepare step
		for _, c := range commands {
			args = append(args, "--prepare", c.prepare, c.cmd)
		}

		cmd := exec.CommandContext(context.Background(), "hyperfine", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		runErr := cmd.Run()
		if runErr != nil {
			_ = os.Remove(tmpFileName)

			return fmt.Errorf("hyperfine failed for count %d: %w", count, runErr)
		}

		// Read markdown output
		mdContent, err := os.ReadFile(tmpFileName)
		_ = os.Remove(tmpFileName) // cleanup temp file

		if err != nil {
			return fmt.Errorf("failed to read hyperfine output: %w", err)
		}

		report.WriteString(fmt.Sprintf("### Dataset: %d tickets\n\n", count))
		report.WriteString(fmt.Sprintf("- workdir: %s\n", workDir))
		report.WriteString(fmt.Sprintf("- warmup: %d\n", cfg.Warmup))
		report.WriteString(fmt.Sprintf("- min-runs: %d\n", cfg.MinRuns))

		if cfg.MaxRuns > 0 {
			report.WriteString(fmt.Sprintf("- max-runs: %d\n", cfg.MaxRuns))
		}

		report.WriteString("\n")
		report.Write(mdContent)
		report.WriteString("\n")

		// Reset ticket to open state after benchmark
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "reopen", "t000001").Run()
	}

	err := os.WriteFile(outFile, []byte(report.String()), 0o600)
	if err != nil {
		return fmt.Errorf("failed to write report: %w", err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", outFile)

	return nil
}

func runCacheStressBench(cfg *Config) error {
	timestamp := time.Now().UTC().Format("20060102-150405")
	outFile := filepath.Join(cfg.OutDir, fmt.Sprintf("ls_cache_stress_%s.md", timestamp))

	var report strings.Builder
	report.WriteString(getSystemInfo())
	report.WriteString("- note: output redirected by hyperfine (default: /dev/null)\n\n")

	for _, count := range cfg.Counts {
		workDir := filepath.Join(cfg.BenchRoot, strconv.Itoa(count))
		ticketDir := filepath.Join(workDir, ".tickets")

		_, statErr := os.Stat(ticketDir)
		if statErr != nil {
			continue
		}

		fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
		fmt.Fprintf(os.Stderr, "CACHE STRESS BENCHMARKS: %d tickets\n", count)
		fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

		cmdLs := fmt.Sprintf("%s -C %s ls --limit=100", cfg.Bin, workDir)

		coldRuns := pickRuns(count, cfg.ColdRunsSmall, cfg.ColdRunsMed, cfg.ColdRunsLarge)
		churnRuns := pickRuns(count, cfg.ChurnRunsSmall, cfg.ChurnRunsMed, cfg.ChurnRunsLarge)
		reconcileRuns := pickRuns(count, cfg.ReconcileRunsSmall, cfg.ReconcileRunsMed, cfg.ReconcileRunsLarge)

		// Reset cache for warm baseline
		_ = os.Remove(filepath.Join(ticketDir, ".cache"))
		_ = os.Remove(filepath.Join(ticketDir, ".cache.lock"))
		_ = os.Remove(filepath.Join(ticketDir, ".bench-trigger"))
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "ls", "--limit=1").Run()

		var results []BenchResult

		// Warm cache baseline
		res, err := benchOne(cfg, "warm (cache hot)", cfg.WarmRuns, "", cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)
		warmMeanMs := res.Mean * 1000

		// Status churn
		churnPrepare := fmt.Sprintf("bash -c '%s -C %s reopen t000001 >/dev/null 2>&1 || true; %s -C %s start t000001 >/dev/null 2>&1 || true; %s -C %s close t000001 >/dev/null 2>&1 || true'",
			cfg.Bin, workDir, cfg.Bin, workDir, cfg.Bin, workDir)

		res, err = benchOne(cfg, "after status churn (reopen/start/close each run)", churnRuns, churnPrepare, cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		res, err = benchOne(cfg, "warm after status churn", cfg.WarmRuns, "", cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		// Cold: delete cache before every run
		coldPrepare := fmt.Sprintf("rm -f '%s/.cache' '%s/.cache.lock'", ticketDir, ticketDir)

		res, err = benchOne(cfg, "cold (rm .cache each run)", coldRuns, coldPrepare, cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		res, err = benchOne(cfg, "warm after cold", cfg.WarmRuns, "", cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		// Corrupt cache
		corruptPrepare := fmt.Sprintf("bash -c 'rm -f \"%s/.cache.lock\"; printf corrupt > \"%s/.cache\"'", ticketDir, ticketDir)

		res, err = benchOne(cfg, "corrupt (invalid .cache each run)", coldRuns, corruptPrepare, cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		// Warm after corrupt rebuild
		_ = os.Remove(filepath.Join(ticketDir, ".cache.lock"))
		_ = os.WriteFile(filepath.Join(ticketDir, ".cache"), []byte("corrupt"), 0o600)
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "ls", "--limit=1").Run()

		res, err = benchOne(cfg, "warm after corrupt rebuild", cfg.WarmRuns, "", cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		// Reconcile setup
		_ = os.Remove(filepath.Join(ticketDir, ".cache"))
		_ = os.Remove(filepath.Join(ticketDir, ".cache.lock"))
		_ = os.Remove(filepath.Join(ticketDir, ".bench-trigger"))
		_ = exec.CommandContext(context.Background(), cfg.Bin, "-C", workDir, "ls", "--limit=1").Run()

		// Force reconcile by making cache mtime old
		reconcilePrepare := fmt.Sprintf("touch -t 200001010000 '%s/.cache'", ticketDir)

		res, err = benchOne(cfg, "reconcile (force dir>cache mtime)", reconcileRuns, reconcilePrepare, cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		res, err = benchOne(cfg, "warm after reconcile", cfg.WarmRuns, "", cmdLs)
		if err != nil {
			return err
		}

		results = append(results, res)

		// Write results table
		report.WriteString(fmt.Sprintf("### Dataset: %d tickets\n\n", count))
		report.WriteString(fmt.Sprintf("- workdir: %s\n", workDir))
		report.WriteString(fmt.Sprintf("- command: `%s`\n", cmdLs))
		report.WriteString(fmt.Sprintf("- warm runs: %d; churn runs: %d; cold/corrupt runs: %d; reconcile runs: %d\n\n",
			cfg.WarmRuns, churnRuns, coldRuns, reconcileRuns))

		report.WriteString("| Scenario | Runs | Mean [ms] | Min [ms] | Max [ms] | Rel |\n")
		report.WriteString("|:---|---:|---:|---:|---:|---:|\n")

		for _, result := range results {
			meanMs := result.Mean * 1000
			minMs := result.Min * 1000
			maxMs := result.Max * 1000
			rel := meanMs / warmMeanMs
			report.WriteString(fmt.Sprintf("| %s | %d | %.2f | %.2f | %.2f | %.2fx |\n",
				result.Label, result.Runs, meanMs, minMs, maxMs, rel))
		}

		report.WriteString("\n")
	}

	err := os.WriteFile(outFile, []byte(report.String()), 0o600)
	if err != nil {
		return fmt.Errorf("failed to write report: %w", err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", outFile)

	return nil
}

func benchOne(cfg *Config, label string, runs int, prepare, cmd string) (BenchResult, error) {
	fmt.Fprintf(os.Stderr, "--- %s ---\n", label)

	tmpFile, err := os.CreateTemp("", "hyperfine-*.json")
	if err != nil {
		return BenchResult{}, fmt.Errorf("failed to create temp file: %w", err)
	}

	_ = tmpFile.Close()

	defer func() { _ = os.Remove(tmpFile.Name()) }()

	args := []string{"-N", "--warmup", strconv.Itoa(cfg.Warmup), "--runs", strconv.Itoa(runs), "--export-json", tmpFile.Name()}
	if prepare != "" {
		args = append(args, "--prepare", prepare)
	}

	args = append(args, cmd)

	hfCmd := exec.CommandContext(context.Background(), "hyperfine", args...)
	hfCmd.Stdout = os.Stdout
	hfCmd.Stderr = os.Stderr

	runErr := hfCmd.Run()
	if runErr != nil {
		return BenchResult{}, fmt.Errorf("hyperfine failed: %w", runErr)
	}

	// Parse JSON output
	jsonData, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return BenchResult{}, fmt.Errorf("failed to read hyperfine output: %w", err)
	}

	var hfResult HyperfineResult

	unmarshalErr := json.Unmarshal(jsonData, &hfResult)
	if unmarshalErr != nil {
		return BenchResult{}, fmt.Errorf("failed to parse hyperfine JSON: %w", unmarshalErr)
	}

	if len(hfResult.Results) == 0 {
		return BenchResult{}, errNoHyperfineResult
	}

	hfRes := hfResult.Results[0]

	return BenchResult{
		Label: label,
		Runs:  runs,
		Mean:  hfRes.Mean,
		Min:   hfRes.Min,
		Max:   hfRes.Max,
	}, nil
}

func pickRuns(count, small, med, large int) int {
	if count <= 10000 {
		return small
	}

	if count <= 100000 {
		return med
	}

	return large
}
