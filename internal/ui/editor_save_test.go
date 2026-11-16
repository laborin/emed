package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// newSaveTestEditor opens path in an Editor attached to a headless app,
// types into it so the tab is dirty, and returns both.
func newSaveTestEditor(t *testing.T, path string) (*tview.Application, *Editor, *EditorTab) {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init screen: %v", err)
	}
	screen.SetSize(120, 40)
	app := tview.NewApplication().SetScreen(screen)

	prevRoot := mainAppRoot
	editor := NewEditor()
	editor.SetApp(app)
	SetMainAppRoot(editor.GetView())
	app.SetRoot(editor.GetView(), true)
	t.Cleanup(func() {
		SetMainAppRoot(prevRoot)
		modalIsOpen = false
		screen.Fini()
	})

	editor.OpenFile(path)
	tab := editor.GetTabByPath(path)
	if tab == nil {
		t.Fatalf("tab for %s not opened", path)
	}
	app.SetFocus(tab.codeEditor)

	tab.codeEditor.PasteHandler()("edited ", func(p tview.Primitive) { app.SetFocus(p) })
	if !tab.IsModified() {
		t.Fatal("tab should be dirty after typing")
	}
	return app, editor, tab
}

func pressFocused(app *tview.Application, key tcell.Key) {
	focused := app.GetFocus()
	if focused == nil {
		return
	}
	if handler := focused.InputHandler(); handler != nil {
		handler(tcell.NewEventKey(key, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	}
}

// requireSaveErrorShown checks that the "Save Failed" dialog is open and focused.
func requireSaveErrorShown(t *testing.T, app *tview.Application) {
	t.Helper()
	if !IsModalOpen() {
		t.Fatal("expected a modal reporting the save error")
	}
	btn, ok := app.GetFocus().(*tview.Button)
	if !ok || btn.GetLabel() != "OK" {
		t.Fatalf("expected the save error dialog OK button focused, got %T", app.GetFocus())
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSaveCurrentFileSuccessMarksTabClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, path, "hello\n")
	_, editor, tab := newSaveTestEditor(t, path)

	var saved string
	editor.SetOnFileSaved(func(p string) { saved = p })

	editor.SaveCurrentFile()

	if tab.IsModified() || tab.codeEditor.IsModified() {
		t.Fatal("tab should be clean after a successful save")
	}
	if saved != path {
		t.Fatalf("onFileSaved got %q, want %q", saved, path)
	}
	if IsModalOpen() {
		t.Fatal("no modal expected after a successful save")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "edited hello\n" {
		t.Fatalf("unexpected content on disk: %q", data)
	}
}

func TestSaveCurrentFileFailureKeepsTabDirtyAndReportsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "hello\n")
	app, editor, tab := newSaveTestEditor(t, path)

	// Replace the parent directory with a regular file: creating the file
	// fails with ENOTDIR, even when the tests run as root.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "")

	saveCalled := false
	editor.SetOnFileSaved(func(string) { saveCalled = true })

	editor.SaveCurrentFile()

	if !tab.IsModified() || !tab.codeEditor.IsModified() {
		t.Fatal("tab must stay dirty when the save fails")
	}
	if saveCalled {
		t.Fatal("onFileSaved must not fire when the save fails")
	}
	if tab.GetPath() != path {
		t.Fatalf("tab path changed to %q", tab.GetPath())
	}
	requireSaveErrorShown(t, app)

	pressFocused(app, tcell.KeyEnter)
	if IsModalOpen() {
		t.Fatal("OK should close the save error dialog")
	}
}

func TestConflictOverwriteFailureReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	writeFile(t, path, "hello\n")
	app, editor, tab := newSaveTestEditor(t, path)

	// The path is now a directory: it exists (so the conflict dialog is
	// shown) but can not be written.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	tab.externallyModified = true

	editor.SaveCurrentFile()
	if !IsModalOpen() {
		t.Fatal("expected the conflict dialog")
	}

	pressFocused(app, tcell.KeyEnter) // Overwrite

	if !tab.IsModified() {
		t.Fatal("tab must stay dirty when overwrite fails")
	}
	if !tab.externallyModified {
		t.Fatal("conflict flag must be kept when overwrite fails")
	}
	requireSaveErrorShown(t, app)
}

func TestConflictSaveAsCopyMovesTabToCopyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "hello\n")
	app, editor, tab := newSaveTestEditor(t, path)
	tab.externallyModified = true

	editor.SaveCurrentFile()
	pressFocused(app, tcell.KeyTab)   // focus "Save as Copy"
	pressFocused(app, tcell.KeyEnter) // select it

	copyPath := filepath.Join(dir, "a_copy.txt")
	if tab.GetPath() != copyPath {
		t.Fatalf("tab path = %q, want %q", tab.GetPath(), copyPath)
	}
	if tab.IsModified() || tab.externallyModified {
		t.Fatal("tab should be clean after saving a copy")
	}
	if editor.GetTabByPath(copyPath) != tab {
		t.Fatal("tab not indexed under the copy path")
	}
	if editor.GetTabByPath(path) != nil {
		t.Fatal("stale index entry left for the original path")
	}
	if IsModalOpen() {
		t.Fatal("no modal expected after a successful copy")
	}
}
