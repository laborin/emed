package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const watcherTestTimeout = 3 * time.Second

// startTestWatcher watches root and forwards every batch of changes to the
// returned channel.
func startTestWatcher(t *testing.T, root string) (*FileWatcher, <-chan []FileChange) {
	t.Helper()

	batches := make(chan []FileChange, 64)
	fw := NewFileWatcher(root)
	fw.SetOnChange(func(changes []FileChange) { batches <- changes })
	if err := fw.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	t.Cleanup(fw.Stop)
	return fw, batches
}

// evalTempDir returns a temp dir with symlinks resolved, so event paths
// match on macOS where /var points to /private/var.
func evalTempDir(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// waitForChange collects batches until one contains path, and returns all
// changes seen so far. It fails the test on timeout.
func waitForChange(t *testing.T, batches <-chan []FileChange, path string) []FileChange {
	t.Helper()

	var seen []FileChange
	deadline := time.After(watcherTestTimeout)
	for {
		select {
		case batch := <-batches:
			seen = append(seen, batch...)
			for _, c := range batch {
				if c.Path == path {
					return seen
				}
			}
		case <-deadline:
			t.Fatalf("no change for %q, got %+v", path, seen)
			return nil
		}
	}
}

func isWatched(fw *FileWatcher, dir string) bool {
	fw.dirMu.RLock()
	defer fw.dirMu.RUnlock()
	return fw.watchedDirs[dir]
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFileWatcherDirectoryRenameMovesWatches(t *testing.T) {
	root := evalTempDir(t)
	oldDir := filepath.Join(root, "src")
	mustMkdirAll(t, filepath.Join(oldDir, "pkg"))

	fw, batches := startTestWatcher(t, root)
	countBefore := fw.GetWatchedDirsCount()

	newDir := filepath.Join(root, "lib")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	waitForChange(t, batches, newDir)

	for _, dir := range []string{oldDir, filepath.Join(oldDir, "pkg")} {
		if isWatched(fw, dir) {
			t.Errorf("old path %q is still watched", dir)
		}
	}
	for _, dir := range []string{newDir, filepath.Join(newDir, "pkg")} {
		if !isWatched(fw, dir) {
			t.Errorf("new path %q is not watched", dir)
		}
	}
	if got := fw.GetWatchedDirsCount(); got != countBefore {
		t.Errorf("watched dirs count drifted from %d to %d", countBefore, got)
	}

	created := filepath.Join(newDir, "pkg", "new.go")
	writeProjectFile(t, root, "lib/pkg/new.go", "package pkg\n")
	seen := waitForChange(t, batches, created)

	for _, c := range seen {
		if strings.HasPrefix(c.Path, oldDir+string(filepath.Separator)) {
			t.Errorf("event reported with stale path %q", c.Path)
		}
	}
}

func TestFileWatcherWatchesSubdirectoriesOfMovedInDirectory(t *testing.T) {
	root := evalTempDir(t)
	outside := evalTempDir(t)
	mustMkdirAll(t, filepath.Join(outside, "incoming", "nested"))

	fw, batches := startTestWatcher(t, root)

	movedIn := filepath.Join(root, "incoming")
	if err := os.Rename(filepath.Join(outside, "incoming"), movedIn); err != nil {
		t.Fatal(err)
	}
	waitForChange(t, batches, movedIn)

	if !isWatched(fw, filepath.Join(movedIn, "nested")) {
		t.Fatal("subdirectory of a moved in directory is not watched")
	}

	created := writeProjectFile(t, root, "incoming/nested/file.txt", "hi\n")
	waitForChange(t, batches, created)
}

func TestFileWatcherDirectoryRenameKeepsOpenTabInSync(t *testing.T) {
	root := evalTempDir(t)
	layout, _ := openForRenameTest(t, root, "src/pkg/a.go", "package pkg\n")

	_, batches := startTestWatcher(t, root)

	oldDir := filepath.Join(root, "src")
	newDir := filepath.Join(root, "lib")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	layout.handleFileChanges(waitForChange(t, batches, newDir))

	newPath := filepath.Join(newDir, "pkg", "a.go")
	paths := layout.editor.GetOpenFilePaths()
	if len(paths) != 1 || paths[0] != newPath {
		t.Fatalf("expected tab remapped to %q, got %v", newPath, paths)
	}

	if err := os.WriteFile(newPath, []byte("package pkg // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(newPath, future, future); err != nil {
		t.Fatal(err)
	}
	layout.handleFileChanges(waitForChange(t, batches, newPath))

	got := layout.editor.tabs[0].codeEditor.buffer.GetContent()
	if !strings.Contains(got, "edited") {
		t.Errorf("open tab did not reload after external edit in renamed dir, content = %q", got)
	}
}
