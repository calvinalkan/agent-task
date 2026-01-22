// sloty is a simple CLI for interacting with slotcache files.
//
// Usage:
//
//	sloty <cache-file>              Open an existing cache file
//	sloty new [opts] <cache-file>   Create a new cache file
//
// Options for 'new' command:
//
//	-k, --key-size      Key size in bytes (default: prompts)
//	-i, --index-size    Index size in bytes (default: prompts)
//	-c, --capacity      Slot capacity (default: prompts)
//	-o, --ordered       Enable ordered-keys mode
//	-v, --version       User version for schema compatibility (default: 1)
//
// Commands (in REPL):
//
//	put <key> [revision] [index]   Insert or update an entry
//	get <key>                      Retrieve an entry by key
//	del <key>                      Delete an entry
//	scan [limit]                   List all entries
//	prefix <prefix> [limit]        Scan entries matching prefix
//	len                            Count live entries
//	gen                            Show current generation
//	info                           Show cache info
//	bulk <count> [prefix]          Insert N random entries
//	seq <count> [start]            Insert N sequential entries
//	bench <count>                  Benchmark put+get performance
//	invalidate                     Invalidate the cache
//	help                           Show this help
//	exit / quit / q                Exit
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/peterh/liner"
)

// Header offsets for reading existing cache files (matches slotcache format).
const (
	slcHeaderSize    = 256
	slcOffMagic      = 0x000
	slcOffKeySize    = 0x00C
	slcOffIndexSize  = 0x010
	slcOffFlags      = 0x01C
	slcOffCapacity   = 0x020
	slcOffUserVer    = 0x038
	slcFlagOrdered   = 0x00000001
)

// cacheConfig holds configuration read from an existing cache file header.
type cacheConfig struct {
	KeySize      int
	IndexSize    int
	SlotCapacity uint64
	OrderedKeys  bool
	UserVersion  uint64
}

// readCacheConfig reads configuration from an existing cache file header.
func readCacheConfig(path string) (cacheConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return cacheConfig{}, err
	}
	defer f.Close()

	header := make([]byte, slcHeaderSize)
	n, err := f.Read(header)
	if err != nil {
		return cacheConfig{}, fmt.Errorf("reading header: %w", err)
	}
	if n < slcHeaderSize {
		return cacheConfig{}, fmt.Errorf("file too small: %d bytes", n)
	}

	// Verify magic
	if !bytes.Equal(header[slcOffMagic:slcOffMagic+4], []byte("SLC1")) {
		return cacheConfig{}, fmt.Errorf("invalid magic: not a slotcache file")
	}

	flags := binary.LittleEndian.Uint32(header[slcOffFlags:])

	return cacheConfig{
		KeySize:      int(binary.LittleEndian.Uint32(header[slcOffKeySize:])),
		IndexSize:    int(binary.LittleEndian.Uint32(header[slcOffIndexSize:])),
		SlotCapacity: binary.LittleEndian.Uint64(header[slcOffCapacity:]),
		OrderedKeys:  (flags & slcFlagOrdered) != 0,
		UserVersion:  binary.LittleEndian.Uint64(header[slcOffUserVer:]),
	}, nil
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return errors.New("missing command or cache file path")
	}

	// Check if first arg is "new" command
	if os.Args[1] == "new" {
		return runNew(os.Args[2:])
	}

	// Otherwise, treat first arg as cache file path (open existing)
	return runOpen(os.Args[1:])
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  sloty <cache-file>              Open an existing cache file\n")
	fmt.Fprintf(os.Stderr, "  sloty new [opts] <cache-file>   Create a new cache file\n")
	fmt.Fprintf(os.Stderr, "\nRun 'sloty new --help' for options when creating a new cache.\n")
}

func runNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)

	keySize := fs.Int("k", 0, "key size in bytes")
	fs.IntVar(keySize, "key-size", 0, "key size in bytes")

	indexSize := fs.Int("i", -1, "index size in bytes")
	fs.IntVar(indexSize, "index-size", -1, "index size in bytes")

	capacity := fs.Uint64("c", 0, "slot capacity")
	fs.Uint64Var(capacity, "capacity", 0, "slot capacity")

	ordered := fs.Bool("o", false, "enable ordered-keys mode")
	fs.BoolVar(ordered, "ordered", false, "enable ordered-keys mode")

	userVersion := fs.Uint64("v", 1, "user version")
	fs.Uint64Var(userVersion, "version", 1, "user version")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: sloty new [options] <cache-file>\n\n")
		fmt.Fprintf(os.Stderr, "Create a new slotcache file. If options are not provided, you will be prompted.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fs.Usage()
		return errors.New("missing cache file path")
	}

	cachePath := fs.Arg(0)

	// Check if file already exists
	if _, err := os.Stat(cachePath); err == nil {
		return fmt.Errorf("cache file already exists: %s (use 'sloty %s' to open it)", cachePath, cachePath)
	}

	reader := bufio.NewReader(os.Stdin)

	// Prompt for values not provided via flags
	if *keySize == 0 {
		*keySize = promptInt(reader, "Key size in bytes", 16)
	}

	if *indexSize < 0 {
		*indexSize = promptInt(reader, "Index size in bytes", 0)
	}

	if *capacity == 0 {
		*capacity = uint64(promptInt(reader, "Slot capacity", 1000))
	}

	// Only prompt for ordered in interactive mode (when other values were prompted)
	wasInteractive := !isFlagSet(fs, "k") && !isFlagSet(fs, "key-size")
	if wasInteractive && !*ordered {
		*ordered = promptBool(reader, "Enable ordered-keys mode", false)
	}

	opts := slotcache.Options{
		Path:         cachePath,
		KeySize:      *keySize,
		IndexSize:    *indexSize,
		SlotCapacity: *capacity,
		OrderedKeys:  *ordered,
		UserVersion:  *userVersion,
	}

	fmt.Printf("\nCreating cache with:\n")
	fmt.Printf("  Path:          %s\n", cachePath)
	fmt.Printf("  Key size:      %d bytes\n", *keySize)
	fmt.Printf("  Index size:    %d bytes\n", *indexSize)
	fmt.Printf("  Capacity:      %d slots\n", *capacity)
	fmt.Printf("  Ordered keys:  %v\n", *ordered)
	fmt.Printf("  User version:  %d\n", *userVersion)
	fmt.Println()

	cache, err := slotcache.Open(opts)
	if err != nil {
		return fmt.Errorf("creating cache: %w", err)
	}
	defer cache.Close()

	repl := &REPL{
		cache:     cache,
		keySize:   *keySize,
		indexSize: *indexSize,
		ordered:   *ordered,
	}

	return repl.Run()
}

func runOpen(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: sloty <cache-file>\n\n")
		fmt.Fprintf(os.Stderr, "Open an existing slotcache file.\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fs.Usage()
		return errors.New("missing cache file path")
	}

	cachePath := fs.Arg(0)

	// Check if file exists
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		return fmt.Errorf("cache file does not exist: %s (use 'sloty new %s' to create it)", cachePath, cachePath)
	}

	// Read configuration from file header
	cfg, err := readCacheConfig(cachePath)
	if err != nil {
		return fmt.Errorf("reading cache config: %w", err)
	}

	// Open the cache with matching configuration
	cache, err := slotcache.Open(slotcache.Options{
		Path:         cachePath,
		KeySize:      cfg.KeySize,
		IndexSize:    cfg.IndexSize,
		SlotCapacity: cfg.SlotCapacity,
		OrderedKeys:  cfg.OrderedKeys,
		UserVersion:  cfg.UserVersion,
	})
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	defer cache.Close()

	repl := &REPL{
		cache:     cache,
		keySize:   cfg.KeySize,
		indexSize: cfg.IndexSize,
		ordered:   cfg.OrderedKeys,
	}

	return repl.Run()
}

// promptInt prompts the user for an integer value with a default.
func promptInt(reader *bufio.Reader, prompt string, defaultVal int) int {
	for {
		fmt.Printf("%s [%d]: ", prompt, defaultVal)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return defaultVal
		}

		val, err := strconv.Atoi(input)
		if err != nil {
			fmt.Println("Please enter a valid integer.")
			continue
		}

		return val
	}
}

// promptBool prompts the user for a boolean value with a default.
func promptBool(reader *bufio.Reader, prompt string, defaultVal bool) bool {
	defaultStr := "n"
	if defaultVal {
		defaultStr = "y"
	}

	for {
		fmt.Printf("%s [%s]: ", prompt, defaultStr)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))

		if input == "" {
			return defaultVal
		}

		switch input {
		case "y", "yes", "true", "1":
			return true
		case "n", "no", "false", "0":
			return false
		default:
			fmt.Println("Please enter y/n.")
		}
	}
}

// isFlagSet checks if a flag was explicitly set on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// REPL is the interactive command loop.
type REPL struct {
	cache     *slotcache.Cache
	keySize   int
	indexSize int
	ordered   bool
	liner     *liner.State
}

// historyFile returns the path to the history file.
func historyFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".sloty_history")
}

// Run starts the REPL loop.
func (r *REPL) Run() error {
	// Set up liner for readline-style input
	r.liner = liner.NewLiner()
	defer r.liner.Close()

	// Configure liner
	r.liner.SetCtrlCAborts(true)
	r.liner.SetCompleter(r.completer)

	// Load history
	if f, err := os.Open(historyFile()); err == nil {
		r.liner.ReadHistory(f)
		f.Close()
	}

	fmt.Printf("sloty - slotcache CLI (key_size=%d, index_size=%d, ordered=%v)\n", r.keySize, r.indexSize, r.ordered)
	fmt.Println("Type 'help' for available commands.")
	fmt.Println()

	for {
		line, err := r.liner.Prompt("sloty> ")
		if err != nil {
			if err == liner.ErrPromptAborted || err == io.EOF {
				fmt.Println("\nBye!")

				break
			}

			return fmt.Errorf("reading input: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Add to history
		r.liner.AppendHistory(line)

		parts := strings.Fields(line)
		cmd := strings.ToLower(parts[0])
		args := parts[1:]

		switch cmd {
		case "exit", "quit", "q":
			fmt.Println("Bye!")

			r.saveHistory()

			return nil

		case "help", "?":
			r.printHelp()

		case "put":
			r.cmdPut(args)

		case "get":
			r.cmdGet(args)

		case "del", "delete":
			r.cmdDelete(args)

		case "scan", "ls", "list":
			r.cmdScan(args)

		case "prefix":
			r.cmdPrefix(args)

		case "len", "count":
			r.cmdLen()

		case "gen", "generation":
			r.cmdGeneration()

		case "info":
			r.cmdInfo()

		case "clear", "cls":
			fmt.Print("\033[H\033[2J")

		case "bulk":
			r.cmdBulk(args)

		case "seq":
			r.cmdSeq(args)

		case "bench":
			r.cmdBench(args)

		case "invalidate":
			r.cmdInvalidate()

		default:
			fmt.Printf("Unknown command: %s (type 'help' for commands)\n", cmd)
		}
	}

	r.saveHistory()

	return nil
}

// saveHistory persists command history to disk.
func (r *REPL) saveHistory() {
	if path := historyFile(); path != "" {
		if f, err := os.Create(path); err == nil {
			r.liner.WriteHistory(f)
			f.Close()
		}
	}
}

// completer provides tab completion for commands.
func (r *REPL) completer(line string) []string {
	commands := []string{
		"put", "get", "del", "delete",
		"scan", "ls", "list", "prefix",
		"len", "count", "gen", "generation",
		"info", "bulk", "seq", "bench",
		"invalidate", "clear", "cls",
		"help", "exit", "quit", "q",
	}

	var completions []string

	lower := strings.ToLower(line)
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, lower) {
			completions = append(completions, cmd)
		}
	}

	return completions
}

func (r *REPL) printHelp() {
	fmt.Println("Commands:")
	fmt.Println("  put <key> [revision] [index]   Insert or update an entry")
	fmt.Println("  get <key>                      Retrieve an entry by key")
	fmt.Println("  del <key>                      Delete an entry")
	fmt.Println("  scan [limit]                   List all entries")
	fmt.Println("  prefix <prefix> [limit]        Scan entries matching prefix")
	fmt.Println("  len                            Count live entries")
	fmt.Println("  gen                            Show current generation")
	fmt.Println("  info                           Show cache info")
	fmt.Println("  bulk <count> [prefix]          Insert N random entries")
	fmt.Println("  seq <count> [start]            Insert N sequential entries")
	fmt.Println("  bench <count>                  Benchmark put+get performance")
	fmt.Println("  invalidate                     Invalidate the cache")
	fmt.Println("  help                           Show this help")
	fmt.Println("  exit / quit / q                Exit")
	fmt.Println()
	fmt.Println("Keys: hex (e.g., 'deadbeef') or plain text (e.g., 'foo').")
	fmt.Println("      Zero-padded or truncated to key_size.")
}

// parseKey parses a key from user input.
// Tries hex first, falls back to plain text.
func (r *REPL) parseKey(s string) ([]byte, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		raw = []byte(s)
	}

	// Pad or truncate to key size
	key := make([]byte, r.keySize)
	copy(key, raw)

	return key, nil
}

// parseIndex parses index bytes from user input.
func (r *REPL) parseIndex(s string) ([]byte, error) {
	if r.indexSize == 0 {
		return []byte{}, nil
	}

	raw, err := hex.DecodeString(s)
	if err != nil {
		raw = []byte(s)
	}

	// Pad or truncate to index size
	index := make([]byte, r.indexSize)
	copy(index, raw)

	return index, nil
}

// formatKey formats a key for display.
func (r *REPL) formatKey(key []byte) string {
	// Try to show as text if printable, otherwise hex
	printable := true

	for _, b := range key {
		if b != 0 && (b < 32 || b > 126) {
			printable = false

			break
		}
	}

	if printable {
		// Trim trailing zeros for display
		end := len(key)
		for end > 0 && key[end-1] == 0 {
			end--
		}

		if end > 0 {
			return fmt.Sprintf("%q", string(key[:end]))
		}
	}

	return hex.EncodeToString(key)
}

// formatIndex formats index bytes for display.
func (r *REPL) formatIndex(index []byte) string {
	if len(index) == 0 {
		return "(none)"
	}

	// Try to show as text if printable
	printable := true

	for _, b := range index {
		if b != 0 && (b < 32 || b > 126) {
			printable = false

			break
		}
	}

	if printable {
		end := len(index)
		for end > 0 && index[end-1] == 0 {
			end--
		}

		if end > 0 {
			return fmt.Sprintf("%q", string(index[:end]))
		}

		return "(empty)"
	}

	return hex.EncodeToString(index)
}

func (r *REPL) cmdPut(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: put <key> [revision] [index]")

		return
	}

	key, err := r.parseKey(args[0])
	if err != nil {
		fmt.Printf("Error parsing key: %v\n", err)

		return
	}

	var revision int64 = 0
	if len(args) >= 2 {
		revision, err = strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Printf("Error parsing revision: %v\n", err)

			return
		}
	}

	var index []byte

	if r.indexSize > 0 {
		if len(args) >= 3 {
			index, err = r.parseIndex(args[2])
			if err != nil {
				fmt.Printf("Error parsing index: %v\n", err)

				return
			}
		} else {
			index = make([]byte, r.indexSize)
		}
	} else {
		if len(args) >= 3 {
			fmt.Printf("Warning: index_size=0, ignoring index argument %q\n", args[2])
		}

		index = []byte{}
	}

	writer, err := r.cache.Writer()
	if err != nil {
		fmt.Printf("Error acquiring writer: %v\n", err)

		return
	}
	defer writer.Close()

	err = writer.Put(key, revision, index)
	if err != nil {
		fmt.Printf("Error staging put: %v\n", err)

		return
	}

	err = writer.Commit()
	if err != nil {
		fmt.Printf("Error committing: %v\n", err)

		return
	}

	fmt.Printf("OK: put %s (revision=%d)\n", r.formatKey(key), revision)
}

func (r *REPL) cmdGet(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: get <key>")

		return
	}

	key, err := r.parseKey(args[0])
	if err != nil {
		fmt.Printf("Error parsing key: %v\n", err)

		return
	}

	entry, found, err := r.cache.Get(key)
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	if !found {
		fmt.Println("(not found)")

		return
	}

	fmt.Printf("Key:      %s\n", r.formatKey(entry.Key))
	fmt.Printf("Revision: %d\n", entry.Revision)
	fmt.Printf("Index:    %s\n", r.formatIndex(entry.Index))
}

func (r *REPL) cmdDelete(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: del <key>")

		return
	}

	key, err := r.parseKey(args[0])
	if err != nil {
		fmt.Printf("Error parsing key: %v\n", err)

		return
	}

	writer, err := r.cache.Writer()
	if err != nil {
		fmt.Printf("Error acquiring writer: %v\n", err)

		return
	}
	defer writer.Close()

	existed, err := writer.Delete(key)
	if err != nil {
		fmt.Printf("Error staging delete: %v\n", err)

		return
	}

	err = writer.Commit()
	if err != nil {
		fmt.Printf("Error committing: %v\n", err)

		return
	}

	if existed {
		fmt.Printf("OK: deleted %s\n", r.formatKey(key))
	} else {
		fmt.Printf("OK: %s did not exist\n", r.formatKey(key))
	}
}

func (r *REPL) cmdScan(args []string) {
	limit := 20

	if len(args) >= 1 {
		var err error

		limit, err = strconv.Atoi(args[0])
		if err != nil {
			fmt.Printf("Error parsing limit: %v\n", err)

			return
		}
	}

	entries, err := r.cache.Scan(slotcache.ScanOptions{Limit: limit})
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	if len(entries) == 0 {
		fmt.Println("(empty)")

		return
	}

	for i, e := range entries {
		fmt.Printf("%3d. %s  rev=%d  idx=%s\n", i+1, r.formatKey(e.Key), e.Revision, r.formatIndex(e.Index))
	}

	if len(entries) == limit {
		fmt.Printf("... (showing first %d, use 'scan <limit>' for more)\n", limit)
	}
}

func (r *REPL) cmdPrefix(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: prefix <prefix> [limit]")

		return
	}

	prefix, err := r.parseKey(args[0])
	if err != nil {
		fmt.Printf("Error parsing prefix: %v\n", err)

		return
	}

	// For prefix matching, trim trailing zeros
	end := len(prefix)
	for end > 0 && prefix[end-1] == 0 {
		end--
	}

	prefix = prefix[:end]

	if len(prefix) == 0 {
		fmt.Println("Error: prefix cannot be empty")

		return
	}

	limit := 20
	if len(args) >= 2 {
		limit, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Printf("Error parsing limit: %v\n", err)

			return
		}
	}

	entries, err := r.cache.ScanPrefix(prefix, slotcache.ScanOptions{Limit: limit})
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	if len(entries) == 0 {
		fmt.Println("(no matches)")

		return
	}

	for i, e := range entries {
		fmt.Printf("%3d. %s  rev=%d  idx=%s\n", i+1, r.formatKey(e.Key), e.Revision, r.formatIndex(e.Index))
	}

	if len(entries) == limit {
		fmt.Printf("... (showing first %d)\n", limit)
	}
}

func (r *REPL) cmdLen() {
	count, err := r.cache.Len()
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	fmt.Printf("Live entries: %d\n", count)
}

func (r *REPL) cmdGeneration() {
	gen, err := r.cache.Generation()
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	fmt.Printf("Generation: %d\n", gen)
}

func (r *REPL) cmdInfo() {
	count, err := r.cache.Len()
	if err != nil {
		fmt.Printf("Error getting len: %v\n", err)

		return
	}

	gen, err := r.cache.Generation()
	if err != nil {
		fmt.Printf("Error getting generation: %v\n", err)

		return
	}

	userHeader, err := r.cache.UserHeader()
	if err != nil {
		fmt.Printf("Error getting user header: %v\n", err)

		return
	}

	fmt.Printf("Cache Info:\n")
	fmt.Printf("  Key size:      %d bytes\n", r.keySize)
	fmt.Printf("  Index size:    %d bytes\n", r.indexSize)
	fmt.Printf("  Ordered keys:  %v\n", r.ordered)
	fmt.Printf("  Live entries:  %d\n", count)
	fmt.Printf("  Generation:    %d\n", gen)
	fmt.Printf("  User flags:    0x%016x\n", userHeader.Flags)

	// Show non-zero user data
	hasUserData := false

	for _, b := range userHeader.Data {
		if b != 0 {
			hasUserData = true

			break
		}
	}

	if hasUserData {
		fmt.Printf("  User data:     %s\n", hex.EncodeToString(userHeader.Data[:]))
	} else {
		fmt.Printf("  User data:     (empty)\n")
	}
}

func (r *REPL) cmdInvalidate() {
	answer, err := r.liner.Prompt("Are you sure you want to invalidate this cache? (yes/no): ")
	if err != nil {
		fmt.Println("Cancelled.")

		return
	}

	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "yes" && answer != "y" {
		fmt.Println("Cancelled.")

		return
	}

	err = r.cache.Invalidate()
	if err != nil {
		if errors.Is(err, slotcache.ErrWriteback) {
			fmt.Println("Warning: invalidation completed but msync failed (durability not guaranteed)")
		} else {
			fmt.Printf("Error: %v\n", err)

			return
		}
	}

	fmt.Println("Cache invalidated. All future operations will fail with ErrInvalidated.")
}

func (r *REPL) cmdBulk(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: bulk <count> [prefix]")

		return
	}

	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 {
		fmt.Printf("Error: count must be a positive integer\n")

		return
	}

	var prefix []byte
	if len(args) >= 2 {
		prefix, _ = r.parseKey(args[1])
		// Trim trailing zeros from prefix
		end := len(prefix)
		for end > 0 && prefix[end-1] == 0 {
			end--
		}

		prefix = prefix[:end]
	}

	writer, err := r.cache.Writer()
	if err != nil {
		fmt.Printf("Error acquiring writer: %v\n", err)

		return
	}
	defer writer.Close()

	start := time.Now()

	for i := range count {
		key := make([]byte, r.keySize)
		copy(key, prefix)
		// Fill remaining bytes with random data
		if len(prefix) < r.keySize {
			rand.Read(key[len(prefix):])
		}

		revision := time.Now().UnixNano()
		index := make([]byte, r.indexSize)

		err = writer.Put(key, revision, index)
		if err != nil {
			fmt.Printf("Error at entry %d: %v\n", i+1, err)

			return
		}
	}

	err = writer.Commit()
	if err != nil {
		fmt.Printf("Error committing: %v\n", err)

		return
	}

	elapsed := time.Since(start)
	rate := float64(count) / elapsed.Seconds()
	fmt.Printf("OK: inserted %d entries in %v (%.0f ops/sec)\n", count, elapsed.Round(time.Millisecond), rate)
}

func (r *REPL) cmdSeq(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: seq <count> [start]")

		return
	}

	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 {
		fmt.Printf("Error: count must be a positive integer\n")

		return
	}

	startNum := uint64(1)
	if len(args) >= 2 {
		startNum, err = strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			fmt.Printf("Error parsing start: %v\n", err)

			return
		}
	}

	writer, err := r.cache.Writer()
	if err != nil {
		fmt.Printf("Error acquiring writer: %v\n", err)

		return
	}
	defer writer.Close()

	start := time.Now()

	for i := range count {
		key := make([]byte, r.keySize)
		// Store sequence number as big-endian for proper ordering
		seqNum := startNum + uint64(i)
		if r.keySize >= 8 {
			binary.BigEndian.PutUint64(key, seqNum)
		} else {
			// For smaller keys, use what fits
			for j := 0; j < r.keySize && j < 8; j++ {
				key[r.keySize-1-j] = byte(seqNum >> (8 * j))
			}
		}

		revision := int64(seqNum)
		index := make([]byte, r.indexSize)

		err = writer.Put(key, revision, index)
		if err != nil {
			fmt.Printf("Error at entry %d: %v\n", i+1, err)

			return
		}
	}

	err = writer.Commit()
	if err != nil {
		fmt.Printf("Error committing: %v\n", err)

		return
	}

	elapsed := time.Since(start)
	rate := float64(count) / elapsed.Seconds()
	fmt.Printf("OK: inserted %d sequential entries in %v (%.0f ops/sec)\n", count, elapsed.Round(time.Millisecond), rate)
}

func (r *REPL) cmdBench(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: bench <count>")

		return
	}

	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 {
		fmt.Printf("Error: count must be a positive integer\n")

		return
	}

	// Generate random keys upfront
	keys := make([][]byte, count)
	for i := range count {
		keys[i] = make([]byte, r.keySize)
		rand.Read(keys[i])
	}

	// Benchmark puts
	fmt.Printf("Benchmarking %d operations...\n", count)

	writer, err := r.cache.Writer()
	if err != nil {
		fmt.Printf("Error acquiring writer: %v\n", err)

		return
	}

	putStart := time.Now()

	for i, key := range keys {
		index := make([]byte, r.indexSize)

		err = writer.Put(key, int64(i), index)
		if err != nil {
			writer.Close()
			fmt.Printf("Error at put %d: %v\n", i+1, err)

			return
		}
	}

	err = writer.Commit()
	if err != nil {
		fmt.Printf("Error committing: %v\n", err)

		return
	}

	putElapsed := time.Since(putStart)

	// Benchmark gets
	getStart := time.Now()
	hits := 0

	for _, key := range keys {
		_, found, err := r.cache.Get(key)
		if err != nil {
			fmt.Printf("Error on get: %v\n", err)

			return
		}

		if found {
			hits++
		}
	}

	getElapsed := time.Since(getStart)

	// Report results
	fmt.Printf("\nResults:\n")
	fmt.Printf("  Puts:  %d ops in %v (%.0f ops/sec)\n",
		count, putElapsed.Round(time.Millisecond), float64(count)/putElapsed.Seconds())
	fmt.Printf("  Gets:  %d ops in %v (%.0f ops/sec), %d hits\n",
		count, getElapsed.Round(time.Millisecond), float64(count)/getElapsed.Seconds(), hits)
}
