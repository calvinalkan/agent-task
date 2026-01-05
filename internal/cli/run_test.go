package cli_test

import (
	"testing"

	"tk/internal/cli"
)

func TestMainHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "no args", args: []string{}},
		{name: "long flag", args: []string{"--help"}},
		{name: "short flag", args: []string{"-h"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, 0; got != want {
				t.Errorf("exitCode=%d, want=%d", got, want)
			}

			if got, want := stderr, ""; got != want {
				t.Errorf("stderr=%q, want=%q", got, want)
			}

			cli.AssertContains(t, stdout, "tk - minimal ticket system")
			cli.AssertContains(t, stdout, "--cwd")
			cli.AssertContains(t, stdout, "create")
			cli.AssertContains(t, stdout, "--description")
			cli.AssertContains(t, stdout, "start")
		})
	}
}
