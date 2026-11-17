package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const helperEnv = "EMED_TEST_RUN_MAIN"

// TestMain lets the test binary run as emed itself, so the hangup test can
// start a real emed process under a pseudo-terminal.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Args = []string{"emed"}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestShutdownSignalsIncludeSIGHUP(t *testing.T) {
	sigs := shutdownSignals()
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(sigs, want) {
			t.Errorf("shutdown signals %v miss %v", sigs, want)
		}
	}
}

func TestHandleShutdownSignal(t *testing.T) {
	tests := []struct {
		sig         os.Signal
		wantCode    int
		wantRestore bool
	}{
		{sig: syscall.SIGHUP, wantCode: 129, wantRestore: false},
		{sig: syscall.SIGTERM, wantCode: 0, wantRestore: true},
		{sig: os.Interrupt, wantCode: 0, wantRestore: true},
	}

	for _, tt := range tests {
		t.Run(tt.sig.String(), func(t *testing.T) {
			stopped, restored := false, false
			code := handleShutdownSignal(tt.sig, func() { stopped = true }, func() { restored = true })

			if !stopped {
				t.Error("stop was not called")
			}
			if restored != tt.wantRestore {
				t.Errorf("restored terminal = %v, want %v", restored, tt.wantRestore)
			}
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

// TestHangupCleansUp runs emed in a pseudo-terminal and closes the terminal,
// like a dropped SSH session. emed must remove its Claude lock file, save the
// session and exit.
func TestHangupCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real emed process")
	}

	home := t.TempDir()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0])
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"HOME="+home,
		"TERM=xterm-256color",
		"SHELL=/bin/sh",
	)
	ptm, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("start emed: %v", err)
	}
	go drain(ptm)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = ptm.Close()
	})

	lockDir := filepath.Join(home, ".claude", "ide")
	waitFor(t, "Claude lock file", func() bool { return len(lockFiles(t, lockDir)) == 1 })

	// Whatever startup wrote does not count: the session must be written
	// again by the shutdown path.
	sessionPath := filepath.Join(projectDir, ".emed", "session.json")
	if err := os.Remove(sessionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	// Closing the master side hangs up the terminal: the kernel sends SIGHUP
	// to emed, which is the session leader.
	if err := ptm.Close(); err != nil {
		t.Fatal(err)
	}

	// Two paths race here and both clean up: the SIGHUP handler (exit 129)
	// and the UI loop failing on the dead terminal (exit 1). Being killed by
	// the signal is the bug.
	select {
	case err := <-exited:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() < 0 {
			t.Fatalf("emed exit = %v, want a handled exit, not death by signal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("emed did not exit after the hangup")
	}

	if locks := lockFiles(t, lockDir); len(locks) != 0 {
		t.Errorf("lock files left after hangup: %v", locks)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Errorf("session not saved on hangup: %v", err)
	}
}

func drain(f *os.File) {
	buf := make([]byte, 4096)
	for {
		if _, err := f.Read(buf); err != nil {
			return
		}
	}
}

func lockFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
