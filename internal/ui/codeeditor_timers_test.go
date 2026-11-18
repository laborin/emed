package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// runTestApp runs a headless app with a CodeEditor as root, so timer
// callbacks have a UI goroutine to run on. Tests must touch the editor only
// through onUI.
func runTestApp(t *testing.T) (*CodeEditor, func(func())) {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init screen: %v", err)
	}
	screen.SetSize(100, 30)

	app := tview.NewApplication().SetScreen(screen)
	editor := NewCodeEditor()
	editor.SetApp(app)
	app.SetRoot(editor, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := app.Run(); err != nil {
			t.Errorf("app run: %v", err)
		}
	}()
	t.Cleanup(func() {
		app.QueueUpdate(editor.Close)
		app.Stop()
		<-done
	})

	onUI := func(f func()) { app.QueueUpdate(f) }
	return editor, onUI
}

// waitOnUI polls cond on the UI goroutine until it is true.
func waitOnUI(t *testing.T, onUI func(func()), what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		onUI(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHighlightTimerRunsOnUIGoroutine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	editor, onUI := runTestApp(t)
	onUI(func() {
		if err := editor.LoadFile(path); err != nil {
			t.Errorf("load: %v", err)
			return
		}
		editor.PasteHandler()("// edit\n", func(tview.Primitive) {})
		// Save as replaces the highlighter while the update timer is pending.
		if err := editor.SaveFile(filepath.Join(dir, "main.go")); err != nil {
			t.Errorf("save as: %v", err)
		}
	})

	// Let the highlight timer fire. Before the fix it read the highlighter
	// from the timer goroutine, which -race reports.
	time.Sleep(300 * time.Millisecond)
	onUI(func() {
		if editor.highlighter == nil {
			t.Error("save as .go should keep a highlighter")
		}
	})
}

func TestBlameTimerShowsBlameOnUIGoroutine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "a.txt"},
		{"-c", "user.name=Blame Test", "-c", "user.email=blame@example.com", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	editor, onUI := runTestApp(t)
	onUI(func() {
		if err := editor.LoadFile(path); err != nil {
			t.Errorf("load: %v", err)
			return
		}
		// A reload resets the cursor while the blame timer is pending.
		// Before the fix the timer read it from its own goroutine.
		editor.SetContent("one\ntwo\n")
	})

	waitOnUI(t, onUI, "blame ghost text", func() bool {
		for _, gt := range editor.ghostTexts {
			if gt.Row == 0 && strings.Contains(strings.Join(gt.Lines, " "), "Blame Test") {
				return true
			}
		}
		return false
	})
}
