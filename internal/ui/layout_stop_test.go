package ui

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rivo/tview"
)

// TestLayoutStopCleansUp covers the shutdown path shared by quit and the
// signal handler: the lock file is removed and the session is written.
func TestLayoutStopCleansUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := t.TempDir()
	file := writeProjectFile(t, root, "main.go", "package main\n")

	l := newSessionTestLayout(t, root)
	l.editor.OpenFile(file)
	l.stopTicker = make(chan bool)
	l.stopFocus = make(chan bool)

	l.claudeIntegration = NewClaudeIntegration(l, l.editor, l.terminal, tview.NewApplication())
	if err := l.claudeIntegration.Start(); err != nil {
		t.Fatalf("start Claude integration: %v", err)
	}
	lockGlob := filepath.Join(home, ".claude", "ide", "*.lock")
	if locks, _ := filepath.Glob(lockGlob); len(locks) != 1 {
		t.Fatalf("expected one lock file after start, got %v", locks)
	}

	l.Stop()

	if locks, _ := filepath.Glob(lockGlob); len(locks) != 0 {
		t.Errorf("lock files left after Stop: %v", locks)
	}
	session, err := l.projectStore.LoadSession()
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(session.OpenFiles) != 1 || session.OpenFiles[0] != "main.go" {
		t.Errorf("session open files = %v, want [main.go]", session.OpenFiles)
	}
}

// TestLayoutStopTwice makes sure a signal arriving while the normal exit path
// runs does not panic by closing the stop channels twice.
func TestLayoutStopTwice(t *testing.T) {
	l := newSessionTestLayout(t, t.TempDir())
	l.stopTicker = make(chan bool)
	l.stopFocus = make(chan bool)

	l.Stop()
	l.Stop()
}

func TestTerminalStopAllEndsShells(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	term := NewTerminal()
	term.NewTab(t.TempDir())
	pid := term.GetCurrentPID()
	if pid <= 0 {
		t.Fatalf("terminal did not start a shell, pid %d", pid)
	}

	term.StopAll()

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell %d still running after StopAll (kill err %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(term.emulators) != 0 {
		t.Errorf("emulators left after StopAll: %d", len(term.emulators))
	}
}
