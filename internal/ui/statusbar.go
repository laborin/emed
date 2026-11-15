// Package ui provides user interface components and managers for the emed editor.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/laborin/emed/internal/config"
	"github.com/laborin/emed/internal/git"
	"github.com/laborin/emed/internal/theme"
	"github.com/rivo/tview"
)

// StatusBar displays project information, git status, and application info.
type StatusBar struct {
	*tview.TextView
	projectPath   string
	gitBranch     string
	gitRepoName   string
	clipboardPath string
	clipboardCut  bool
	watchedDirs   int
	app           *tview.Application
	leftText      string
	rightText     string
	gitUpdating   bool
	rssBytes      uint64
	rssMu         sync.RWMutex
	rssStopChan   chan struct{}
}

// NewStatusBar creates a new status bar with project information.
func NewStatusBar() *StatusBar {
	sb := &StatusBar{
		TextView:    tview.NewTextView(),
		rssStopChan: make(chan struct{}),
	}

	sb.
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetBackgroundColor(theme.Current.StatusBarBg)

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	sb.projectPath = filepath.Base(cwd)

	sb.updateRSS()

	go sb.updateGitBranch()
	go sb.rssMonitorLoop()

	sb.updateTextParts()

	return sb
}

func (sb *StatusBar) updateRSS() {
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err != nil {
		return
	}
	rss := uint64(rusage.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	sb.rssMu.Lock()
	sb.rssBytes = rss
	sb.rssMu.Unlock()
}

func (sb *StatusBar) rssMonitorLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sb.rssStopChan:
			return
		case <-ticker.C:
			sb.updateRSS()
			sb.updateTextParts()
			if sb.app != nil {
				sb.app.QueueUpdateDraw(func() {})
			}
		}
	}
}

// Stop stops the RSS monitor goroutine.
func (sb *StatusBar) Stop() {
	close(sb.rssStopChan)
}

func (sb *StatusBar) updateGitBranch() {
	if sb.gitUpdating {
		return
	}
	sb.gitUpdating = true
	defer func() { sb.gitUpdating = false }()

	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		sb.gitBranch = ""
		sb.updateTextParts()
		if sb.app != nil {
			sb.app.QueueUpdateDraw(func() {})
		}
		return
	}

	cmd = exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		sb.gitBranch = strings.TrimSpace(string(output))
	} else {
		sb.gitBranch = "detached"
	}

	sb.updateTextParts()
	if sb.app != nil {
		sb.app.QueueUpdateDraw(func() {})
	}
}

func (sb *StatusBar) updateTextParts() {
	var parts []string

	parts = append(parts, fmt.Sprintf("project: %s", sb.projectPath))

	if sb.gitBranch != "" {
		if sb.gitRepoName != "" {
			parts = append(parts, fmt.Sprintf("git: %s (%s)", sb.gitRepoName, sb.gitBranch))
		} else {
			parts = append(parts, fmt.Sprintf("git: %s", sb.gitBranch))
		}
	}

	if sb.clipboardPath != "" {
		clipboardName := filepath.Base(sb.clipboardPath)
		if sb.clipboardCut {
			parts = append(parts, fmt.Sprintf("file operation: [red]cut[-] (%s)", clipboardName))
		} else {
			parts = append(parts, fmt.Sprintf("file operation: [green]copy[-] (%s)", clipboardName))
		}
	}

	if sb.watchedDirs > 0 {
		parts = append(parts, fmt.Sprintf("watching: %d dirs", sb.watchedDirs))
	}

	sb.rssMu.RLock()
	rss := sb.rssBytes
	sb.rssMu.RUnlock()
	if rss > 0 {
		parts = append(parts, fmt.Sprintf("rss: %s", sb.formatRSS(rss)))
	}

	parts = append(parts, "[blue]F1 for help[-]")

	sb.leftText = strings.Join(parts, " | ")

	sb.rightText = fmt.Sprintf("%s v%s", config.AppName, config.Version())
}

func (sb *StatusBar) formatRSS(b uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1fG", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.1fM", float64(b)/MB)
	default:
		return fmt.Sprintf("%.1fK", float64(b)/KB)
	}
}

// Draw renders the status bar to the screen.
func (sb *StatusBar) Draw(screen tcell.Screen) {
	sb.TextView.Draw(screen)

	x, y, width, _ := sb.GetInnerRect()
	if width <= 0 {
		return
	}

	leftLen := tview.TaggedStringWidth(sb.leftText)
	rightLen := len(sb.rightText)

	spacingNeeded := width - leftLen - rightLen - 1

	var fullText string
	if spacingNeeded > 0 {
		spacing := strings.Repeat(" ", spacingNeeded)
		fullText = sb.leftText + spacing + sb.rightText
	} else {
		if width > rightLen+1 {
			fullText = sb.leftText + " " + sb.rightText
		} else {
			fullText = sb.rightText
		}
	}

	style := tcell.StyleDefault.Background(theme.Current.StatusBarBg)
	for i := 0; i < width; i++ {
		screen.SetContent(x+i, y, ' ', nil, style)
	}

	tview.Print(screen, fullText, x, y, width, tview.AlignLeft, tcell.ColorWhite)
}

// GetView returns the status bar as a primitive.
func (sb *StatusBar) GetView() tview.Primitive {
	return sb
}

// SetProjectPath sets the project path displayed in the status bar.
func (sb *StatusBar) SetProjectPath(path string) {
	sb.projectPath = filepath.Base(path)
	sb.updateTextParts()
}

// RefreshGitBranch updates the git branch information.
func (sb *StatusBar) RefreshGitBranch() {
	go sb.updateGitBranch()
}

// UpdateGitBranchForFile updates the git branch based on the file's repository.
func (sb *StatusBar) UpdateGitBranchForFile(filePath string) {
	if filePath == "" {
		return
	}
	go func() {
		branch := git.GetBranch(filePath)
		repoRoot := git.FindRepoRoot(filePath)
		repoName := ""
		if repoRoot != "" {
			repoName = filepath.Base(repoRoot)
		}

		if branch != sb.gitBranch || repoName != sb.gitRepoName {
			sb.gitBranch = branch
			sb.gitRepoName = repoName
			sb.updateTextParts()
			if sb.app != nil {
				sb.app.QueueUpdateDraw(func() {})
			}
		}
	}()
}

// SetClipboard updates the clipboard information in the status bar.
func (sb *StatusBar) SetClipboard(path string, isCut bool) {
	sb.clipboardPath = path
	sb.clipboardCut = isCut
	sb.updateTextParts()
}

// SetApp sets the application reference for the status bar.
func (sb *StatusBar) SetApp(app *tview.Application) {
	sb.app = app
}

// SetWatchedDirs updates the count of watched directories.
func (sb *StatusBar) SetWatchedDirs(count int) {
	sb.watchedDirs = count
	sb.updateTextParts()
}
