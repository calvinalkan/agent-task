package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMainHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no args", args: []string{"tk"}},
		{name: "long flag", args: []string{"tk", "--help"}},
		{name: "short flag", args: []string{"tk", "-h"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, testCase.args, nil)

			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}

			out := stdout.String()

			// Check main usage present
			if !strings.Contains(out, "tk - minimal ticket system") {
				t.Errorf("stdout should contain title")
			}

			// Check global options present
			if !strings.Contains(out, "--cwd") {
				t.Errorf("stdout should contain --cwd option")
			}

			// Check commands present with create options inlined
			if !strings.Contains(out, "create") {
				t.Errorf("stdout should contain create command")
			}

			if !strings.Contains(out, "--description") {
				t.Errorf("stdout should contain create's --description option")
			}

			if !strings.Contains(out, "start") {
				t.Errorf("stdout should contain start command")
			}
		})
	}
}
