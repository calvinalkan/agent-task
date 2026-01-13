package fs_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func TestAtomicWriteFile_DurableAfterCrash(t *testing.T) {
	t.Parallel()

	crash, err := fs.NewCrash(t, fs.NewReal(), &fs.CrashConfig{})
	if err != nil {
		t.Fatalf("fs.NewCrash: %v", err)
	}

	writer := fs.NewAtomicWriter(crash)

	err = writer.WriteWithDefaults("final.txt", strings.NewReader(testContentHello))
	if err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	got, err := crash.ReadFile("final.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != testContentHello {
		t.Fatalf("content=%q, want %q", string(got), testContentHello)
	}
}
