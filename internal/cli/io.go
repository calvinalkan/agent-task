package cli

import (
	"fmt"
	"io"
)

// IO handles command output with LLM-friendly warning visibility.
type IO struct {
	out      io.Writer
	errOut   io.Writer
	warnings []string
	started  bool
}

// NewIO creates a new IO instance.
func NewIO(out, errOut io.Writer) *IO {
	return &IO{out: out, errOut: errOut}
}

// WarnLLM adds an actionable warning for LLM visibility.
//
// Parameters:
//   - issue: what went wrong
//   - action: what the LLM should do about it
//
// Warnings are printed to stderr at both the START and END of output,
// ensuring visibility regardless of truncation or piping (head/tail).
// Any warnings cause exit code 1 to signal attention is needed.
//
// Output to stdout (via Println) still occurs - warnings don't suppress
// normal output. This allows partial results with issues flagged.
func (o *IO) WarnLLM(issue string, action string) {
	o.warnings = append(o.warnings, fmt.Sprintf("%s: %s", issue, action))
}

// Println writes to stdout. On first call, any collected warnings
// are printed to stderr first.
func (o *IO) Println(a ...any) {
	o.flushWarningsStart()
	_, _ = fmt.Fprintln(o.out, a...)
}

// Printf writes formatted output to stdout. On first call, any collected
// warnings are printed to stderr first.
func (o *IO) Printf(format string, a ...any) {
	o.flushWarningsStart()
	_, _ = fmt.Fprintf(o.out, format, a...)
}

// Finish prints warnings to stderr and returns exit code.
// Returns 1 if any warnings, 0 otherwise.
func (o *IO) Finish() int {
	// If no output happened but we have warnings, print them at "start" position
	o.flushWarningsStart()

	// Always print at end
	for _, w := range o.warnings {
		_, _ = fmt.Fprintln(o.errOut, "warning:", w)
	}

	if len(o.warnings) > 0 {
		return 1
	}

	return 0
}

func (o *IO) flushWarningsStart() {
	if !o.started && len(o.warnings) > 0 {
		for _, w := range o.warnings {
			_, _ = fmt.Fprintln(o.errOut, "warning:", w)
		}

		o.started = true
	}
}
