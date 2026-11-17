// Package ui provides the user interface components for the text editor.
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/laborin/emed/internal/claude"
	"github.com/laborin/emed/internal/core/pathutil"
	"github.com/laborin/emed/internal/git"
	"github.com/laborin/emed/internal/logging"
	"github.com/laborin/emed/internal/project"
	"github.com/laborin/emed/internal/theme"
	"github.com/rivo/tview"
)

// MouseFocusDetector wraps a primitive to detect mouse focus changes.
type MouseFocusDetector struct {
	tview.Primitive
	layout *Layout
}

// MouseHandler returns a mouse handler that detects focus changes.
func (m *MouseFocusDetector) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		if handler := m.Primitive.MouseHandler(); handler != nil {
			consumed, capture = handler(action, event, setFocus)
		}

		if action == tview.MouseLeftClick || action == tview.MouseLeftDown {
			go func() {
				time.Sleep(10 * time.Millisecond)
				m.layout.app.QueueUpdateDraw(func() {
					m.layout.checkFocusChange()
				})
			}()
		}

		return consumed, capture
	}
}

// Layout manages the overall application layout and component coordination.
type Layout struct {
	app         *tview.Application
	root        tview.Primitive
	mainArea    *tview.Flex
	rightSide   *tview.Flex
	fileManager *FileManager
	editor      *Editor
	terminal    *Terminal
	statusBar   *StatusBar
	fileWatcher *FileWatcher
	stopTicker  chan bool
	stopFocus   chan bool
	stopOnce    sync.Once

	focusedPanel int
	lastFocus    tview.Primitive

	terminalPosition string
	proportionIndex  int
	proportionDir    int

	// Project search
	projectSearchModal        *ProjectSearchModal
	projectSearchModalWrapper *modalWrapper
	projectSearchState        struct {
		searchTerm    string
		replaceTerm   string
		caseSensitive bool
		wholeWord     bool
		useRegex      bool
	}

	// Claude Code integration (WebSocket)
	claudeIntegration *ClaudeIntegration
	activeDiffView    *DiffView
	diffModalWrapper  *modalWrapper
	pendingDiffs      []*DiffView

	// Dynamic sizing
	lastTerminalWidth int
	fileManagerWidth  int

	// Host terminal screen, captured on draw to forward bell sounds
	hostScreen   tcell.Screen
	hostScreenMu sync.Mutex

	// Git repositories
	gitRepos []git.Repository

	// Launch Claude on start
	launchClaudeOnStart bool

	// Project scoped config and state, nil in single file mode
	projectStore *project.Store
	starterTabs  map[string]int
	restoring    bool

	// Inodes of open files, used to pair external rename events with
	// their new path (the inode survives the rename).
	openFileInodes map[string]uint64

	// Single file mode
	singleFileMode    bool
	startupFile       string
	singleFileWatcher *SingleFileWatcher
}

type LayoutOptions struct {
	SingleFileMode bool
	StartupFile    string
}

// NewLayout creates a new Layout with all UI components.
func NewLayout(app *tview.Application, opts LayoutOptions) *Layout {
	l := &Layout{
		app:              app,
		focusedPanel:     0,
		terminalPosition: "bottom",
		proportionIndex:  1,
		proportionDir:    1,
		fileManagerWidth: 30,
		singleFileMode:   opts.SingleFileMode,
		startupFile:      opts.StartupFile,
		openFileInodes:   make(map[string]uint64),
	}

	l.editor = NewEditor()
	if l.singleFileMode {
		l.editor.SetSingleFileMode(true)
	} else {
		l.fileManager = NewFileManager()
		l.terminal = NewTerminal()
		l.statusBar = NewStatusBar()
		if cwd, err := os.Getwd(); err == nil {
			l.projectStore = project.NewStore(cwd)
			l.projectStore.SetOnDirCreated(func() {
				if l.fileManager != nil {
					l.fileManager.RefreshDirectory(l.projectStore.Root())
				}
			})
		}
		l.starterTabs = make(map[string]int)
	}

	l.initialize()
	return l
}

func (l *Layout) initialize() {
	l.editor.SetApp(l.app)
	SetOnModalClosed(func() {
		// Deferred to the next event loop tick: modal chains (like the
		// repo selector that opens lazygit right after closing) must
		// finish opening their next modal before we check the screen.
		go l.app.QueueUpdateDraw(func() {
			l.showNextPendingDiff()
		})
	})
	if l.singleFileMode {
		l.initializeSingleFileMode()
		return
	}

	l.terminal.SetApp(l.app)
	l.statusBar.SetApp(l.app)

	l.editor.SetOnLastTabClosed(func() {
		l.rebuildLayout()
		// Defer focus setting to ensure UI is ready
		go func() {
			l.app.QueueUpdate(func() {
				l.focusFileManager()
			})
		}()
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyOpenEditorsChanged()
		}
	})

	l.editor.SetOnTabClosed(func(path string) {
		delete(l.openFileInodes, path)
		if l.fileWatcher != nil {
			l.fileWatcher.RemoveOpenFile(path)
			l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
		}
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyOpenEditorsChanged()
		}
		l.saveSession()
	})

	l.terminal.SetOnLastTabClosed(func() {
		l.rebuildLayout()
		if l.editor.HasTabs() {
			l.focusEditor()
		} else {
			l.focusFileManager()
		}
	})

	l.terminal.SetOnHostBell(func() {
		l.beepHostTerminal()
	})

	l.fileManager.SetFilePreviewHandler(func(path string) {
		l.OpenFilePreview(path)
	})

	l.fileManager.SetFileSelectedHandler(func(path string) {
		l.OpenFile(path)
	})

	l.fileManager.SetClipboardChangeHandler(func(path string, isCut bool) {
		l.statusBar.SetClipboard(path, isCut)
	})

	l.fileManager.SetFileRenamedHandler(func(oldPath, newPath string) {
		l.editor.UpdateFilePath(oldPath, newPath)
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyFileRenamed(oldPath, newPath)
		}
	})

	l.fileManager.SetFileDeletedHandler(func(path string) {
		l.editor.CloseFileTab(path)
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyFileDeleted(path)
		}
	})

	l.fileManager.SetFileCreatedHandler(func(path string) {
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyFileCreated(path)
		}
	})

	l.editor.SetOnActiveFileChanged(func(path string) {
		l.statusBar.UpdateGitBranchForFile(path)
		l.fileManager.RevealFile(path)
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyOpenEditorsChanged()
		}
		l.saveSession()
	})

	l.root = tview.NewFlex().SetDirection(tview.FlexRow)

	l.mainArea = tview.NewFlex().SetDirection(tview.FlexColumn)
	l.mainArea.SetBackgroundColor(theme.Current.AppBg)
	l.mainArea.SetDrawFunc(func(screen tcell.Screen, x, y, width, height int) (int, int, int, int) {
		l.captureHostScreen(screen)
		l.checkAndUpdateWidth()
		return x, y, width, height
	})

	l.rightSide = tview.NewFlex()
	l.rightSide.SetBackgroundColor(theme.Current.AppBg)

	l.rebuildLayout()

	l.claudeIntegration = NewClaudeIntegration(l, l.editor, l.terminal, l.app)
	if err := l.claudeIntegration.Start(); err != nil {
		logging.Error("Failed to start Claude Code integration: %v", err)
	}

	l.editor.SetOnSelectionChange(func(path string, text string, startRow, startCol, endRow, endCol int) {
		if l.claudeIntegration != nil {
			l.claudeIntegration.UpdateLatestSelection(&claude.Selection{
				FilePath: path,
				Text:     text,
				Range: claude.Range{
					Start: claude.Position{Line: startRow, Character: startCol},
					End:   claude.Position{Line: endRow, Character: endCol},
				},
			})
		}
	})

	l.editor.SetOnFileSaved(func(path string) {
		l.recordInode(path)
		if l.claudeIntegration != nil {
			l.claudeIntegration.NotifyFileSaved(path)
		}
	})

	mainLayout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(l.mainArea, 0, 1, true).
		AddItem(l.statusBar.GetView(), 1, 0, false)
	mainLayout.SetBackgroundColor(theme.Current.AppBg)

	rootWithMouse := &MouseFocusDetector{
		Primitive: mainLayout,
		layout:    l,
	}

	l.root = rootWithMouse

	SetMainAppRoot(l.root)

	l.fileManager.SetApp(l.app)

	l.setupKeybindings()

	l.scanGitRepositories()

	l.setupFileWatcher()

	l.restoreSession()

	l.setupFocusDetection()

	go func() {
		time.Sleep(10 * time.Millisecond)
		l.app.QueueUpdateDraw(func() {
			if l.editor.HasTabs() {
				l.focusEditor()
			} else {
				l.focusFileManager()
			}
		})
	}()

	go func() {
		time.Sleep(100 * time.Millisecond)
		l.app.QueueUpdateDraw(func() {
			if l.launchClaudeOnStart {
				l.LaunchClaude()
			}
		})
	}()
}

func (l *Layout) initializeSingleFileMode() {
	l.mainArea = tview.NewFlex().SetDirection(tview.FlexColumn)
	l.mainArea.AddItem(l.editor.GetView(), 0, 1, true)

	l.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(l.mainArea, 0, 1, true)

	SetMainAppRoot(l.root)

	l.setupKeybindings()
	l.setupFocusDetection()

	if l.startupFile != "" {
		l.OpenFile(l.startupFile)
		l.setupSingleFileWatcher(l.startupFile)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		l.app.QueueUpdateDraw(func() {
			l.focusEditor()
		})
	}()
}

// GetRoot returns the root primitive for the layout.
func (l *Layout) GetRoot() tview.Primitive {
	return l.root
}

const maxFileManagerWidth = 45

func (l *Layout) captureHostScreen(screen tcell.Screen) {
	l.hostScreenMu.Lock()
	l.hostScreen = screen
	l.hostScreenMu.Unlock()
}

func (l *Layout) beepHostTerminal() {
	l.hostScreenMu.Lock()
	screen := l.hostScreen
	l.hostScreenMu.Unlock()

	if screen != nil {
		screen.Beep()
	}
}

func (l *Layout) calculateFileManagerWidth(terminalWidth int) int {
	if terminalWidth > 150 {
		width := terminalWidth / 5
		if width > maxFileManagerWidth {
			return maxFileManagerWidth
		}
		return width
	}
	return 30
}

func (l *Layout) checkAndUpdateWidth() {
	_, _, width, _ := l.mainArea.GetRect()
	if width == l.lastTerminalWidth {
		return
	}
	l.lastTerminalWidth = width

	newWidth := l.calculateFileManagerWidth(width)
	if newWidth != l.fileManagerWidth {
		l.fileManagerWidth = newWidth
		l.rebuildLayout()
	}
}

func (l *Layout) rebuildLayout() {
	if l.singleFileMode {
		l.mainArea.Clear()
		l.mainArea.AddItem(l.editor.GetView(), 0, 1, true)
		return
	}

	l.rightSide.Clear()

	l.mainArea.Clear()

	hasEditorTabs := l.editor.HasTabs()
	hasTerminalTabs := l.terminal.HasTabs()

	if !hasEditorTabs && !hasTerminalTabs {
		noDocsView := l.editor.GetNoDocsView()
		if box, ok := noDocsView.(*tview.Box); ok {
			box.SetBorder(false)
		}
		l.rightSide.SetDirection(tview.FlexRow).
			Clear().
			AddItem(noDocsView, 0, 1, false)
	} else if hasEditorTabs && !hasTerminalTabs {
		l.rightSide.SetDirection(tview.FlexRow).
			Clear().
			AddItem(l.editor.GetView(), 0, 1, false)
	} else if !hasEditorTabs && hasTerminalTabs {
		l.rightSide.SetDirection(tview.FlexRow).
			Clear().
			AddItem(l.terminal.GetView(), 0, 1, false)
	} else {
		proportions := [][2]int{
			{8, 2},
			{7, 3},
			{6, 4},
			{5, 5},
			{4, 6},
			{3, 7},
			{2, 8},
		}

		editorProp := proportions[l.proportionIndex][0]
		terminalProp := proportions[l.proportionIndex][1]

		if l.terminalPosition == "bottom" {
			l.rightSide.SetDirection(tview.FlexRow).
				Clear().
				AddItem(l.editor.GetView(), 0, editorProp, false).
				AddItem(l.terminal.GetView(), 0, terminalProp, false)
		} else {
			l.rightSide.SetDirection(tview.FlexColumn).
				Clear().
				AddItem(l.editor.GetView(), 0, editorProp, false).
				AddItem(l.terminal.GetView(), 0, terminalProp, false)
		}
	}

	l.mainArea.
		AddItem(l.fileManager.GetView(), l.fileManagerWidth, 0, false).
		AddItem(l.rightSide, 0, 1, false)
}

func (l *Layout) cycleProportion() {
	if !l.terminal.HasTabs() {
		return
	}

	l.proportionIndex += l.proportionDir

	if l.proportionIndex >= 7 {
		l.proportionIndex = 5
		l.proportionDir = -1
	} else if l.proportionIndex < 0 {
		l.proportionIndex = 1
		l.proportionDir = 1
	}

	l.rebuildLayout()
}

func (l *Layout) togglePosition() {
	if !l.terminal.HasTabs() {
		return
	}

	if l.terminalPosition == "bottom" {
		l.terminalPosition = "right"
	} else {
		l.terminalPosition = "bottom"
	}
	l.rebuildLayout()
}

func (l *Layout) getHelpText() string {
	if l.singleFileMode {
		return `[::b]EMED - Emmanuel's Editor [single-file mode][-::-]

[blue]EDITOR:[-]
  Ctrl+S         Save File
  Ctrl+F         Find/Replace in Document
  Ctrl+Z         Undo
  Ctrl+Y         Redo
  Ctrl+C         Copy Selection
  Ctrl+X         Cut Selection
  Ctrl+V         Paste
  F10            Toggle Line Wrap
  Arrow Keys     Navigate
  Shift+Arrows   Select Text

[blue]GENERAL:[-]
  F1             Show This Help
  F8             Show Performance Stats
  Ctrl+Q         Quit

[::d]Press ESC or click outside to close[-::-]`
	}

	return `[::b]KEYBOARD SHORTCUTS[-::-]

[blue]FILE MANAGER (when focused):[-]
  Enter          Open File / Toggle Directory
  Ctrl+I         File Info Dialog
  Ctrl+N         Create New File
  Ctrl+Alt+N     Create New Directory
  Ctrl+C         Copy File/Directory
  Ctrl+X         Cut File/Directory  
  Ctrl+V         Paste File/Directory
  Ctrl+D         Delete File/Directory
  Ctrl+R         Rename File/Directory
  
[blue]EDITOR:[-]
  Ctrl+S         Save File
  Ctrl+W         Close Current Tab
  Ctrl+F         Find/Replace in Document
  Ctrl+Z         Undo
  Ctrl+Y         Redo
  F10            Toggle Line Wrap
  
[blue]NAVIGATION:[-]
  Ctrl+A         Focus File Manager
  Ctrl+E         Focus Editor
  Ctrl+T         Focus/Create Terminal
  F2             Cycle Focus Between Panels
  
[blue]TERMINAL:[-]
  Alt+T          Toggle Terminal Mode (Passthrough/Application)
  
[blue]TABS:[-]
  Ctrl+Shift+←   Previous Tab (or F3, Ctrl+Alt+←)
  Ctrl+Shift+→   Next Tab (or F4, Ctrl+Alt+→)
  F3             Previous Tab
  F4             Next Tab
  
[blue]LAYOUT:[-]
  Ctrl+L         Cycle Proportions (or F7)
  Ctrl+P         Toggle Terminal Position (or F6)
  F6             Toggle Terminal Position
  F7             Cycle Proportions
  
[blue]GIT:[-]
  Ctrl+G         Open Lazygit (requires lazygit installed)
  Ctrl+B         Show Git Blame for Current Line (in editor)

[blue]GENERAL:[-]
  F1             Show This Help
  F8             Show Performance Stats
  F9             Launch Claude Code
  F12            Terminal Starters
  Ctrl+Q         Quit

[blue]FILE MANAGEMENT:[-]
When the file manager has focus, you can manage your files easily. Use arrow keys to navegate through your project tree. Press Ctrl+N to create new files right were you need them. Copy files with Ctrl+C, then move to another folder and paste with Ctrl+V. Delete unwanted files with Ctrl+D - but be carefull, there's no undo for this! Rename files with Ctrl+R and use Ctrl+X to cut (move) files in combination with Ctrl+V. Type to highlight the file that matches your input.

[blue]TERMINAL PASSTHROUGH MODE:[-]
The terminal has two modes. Normal mode lets you use keyboard shortcuts normaly. But sometimes you need to send special keys directly to the terminal program (like when using vim or other CLI tools). Press Alt+T to toggle passthrough mode. In this mode, all keystrokes go straight to the terminal - only Alt+T works to switch back. The terminal tab title shows when passthru mode is active for that particular tab with an asterisk.

[::d]Press ESC or click outside to close[-::-]`
}

func (l *Layout) showHelp() {
	ShowHelpModal(l.app, l.getHelpText())
}

func (l *Layout) showBlameModal() {
	filePath, lineNum := l.editor.GetCurrentFileAndLine()
	if filePath == "" || lineNum == 0 {
		ShowAlertModal(l.app, "Info", "No file open")
		return
	}

	blameInfo, err := git.GetBlameForLine(filePath, lineNum)
	if err != nil {
		ShowAlertModal(l.app, "Info", "No git blame data")
		return
	}

	repoRoot := git.FindRepoRoot(filePath)
	repoName := ""
	if repoRoot != "" {
		repoName = filepath.Base(repoRoot)
	}

	blameText := git.FormatDetailedBlameInfo(blameInfo, filePath, lineNum, repoName)
	ShowBlameModalWithCommit(l.app, blameText, blameInfo.Hash, filePath)
}

func (l *Layout) setupKeybindings() {
	if l.singleFileMode {
		l.setupSingleFileKeybindings()
		return
	}

	l.app.SetInputCapture(l.captureMainModeInput)
}

func (l *Layout) captureMainModeInput(event *tcell.EventKey) *tcell.EventKey {
	if IsModalOpen() {
		return event
	}

	l.syncFocusedPanelWithAppFocus()

	if l.focusedPanel == 2 && l.terminal.IsInPassthroughMode() {
		return event
	}

	if result, handled := l.handleMainModeFunctionKeys(event); handled {
		return result
	}

	if result, handled := l.handleMainModeTabNavigationShortcut(event); handled {
		return result
	}

	if result, handled := l.handleMainModeCtrlShortcuts(event); handled {
		return result
	}

	return event
}

func (l *Layout) syncFocusedPanelWithAppFocus() {
	currentFocus := l.app.GetFocus()
	if l.fileManager.HasFocus(currentFocus) {
		l.focusedPanel = 0
	} else if l.editor.HasFocus(currentFocus) {
		l.focusedPanel = 1
	} else if l.terminal.HasFocus(currentFocus) {
		l.focusedPanel = 2
	}
}

func (l *Layout) handleMainModeFunctionKeys(event *tcell.EventKey) (*tcell.EventKey, bool) {
	switch event.Key() {
	case tcell.KeyF1:
		l.showHelp()
		return nil, true
	case tcell.KeyF2:
		l.cyclePanelFocus()
		return nil, true
	case tcell.KeyF3:
		if l.focusedPanel == 1 {
			if l.editor.IsSearchBarActive() {
				return event, false
			}
			l.editor.PreviousTab()
		} else if l.focusedPanel == 2 {
			l.terminal.PreviousTab()
		}
		return nil, true
	case tcell.KeyF4:
		if l.focusedPanel == 1 {
			if l.editor.IsSearchBarActive() {
				return event, false
			}
			l.editor.NextTab()
		} else if l.focusedPanel == 2 {
			l.terminal.NextTab()
		}
		return nil, true
	case tcell.KeyF5:
		if l.focusedPanel == 0 {
			l.fileManager.RefreshCurrentDirectory()
		}
		return nil, true
	case tcell.KeyF6:
		if l.terminal.HasTabs() {
			l.togglePosition()
		}
		return nil, true
	case tcell.KeyF7:
		if l.terminal.HasTabs() {
			l.cycleProportion()
		}
		return nil, true
	case tcell.KeyF8:
		ShowMemStatsModal(l.app, l.getWatcherStatsProvider())
		return nil, true
	case tcell.KeyF9:
		l.LaunchClaude()
		return nil, true
	case tcell.KeyF10:
		if l.focusedPanel == 1 {
			l.editor.ToggleLineWrap()
			return nil, true
		}
		return event, false
	case tcell.KeyF12:
		l.showStarters()
		return nil, true
	default:
		return event, false
	}
}

func (l *Layout) cyclePanelFocus() {
	hasEditor := l.editor.HasTabs()
	hasTerminal := l.terminal.HasTabs()

	if !hasEditor && !hasTerminal {
		l.focusFileManager()
		return
	}

	if !hasEditor {
		if l.focusedPanel == 0 {
			l.focusTerminal()
		} else {
			l.focusFileManager()
		}
		return
	}

	if !hasTerminal {
		if l.focusedPanel == 0 {
			l.focusEditor()
		} else {
			l.focusFileManager()
		}
		return
	}

	nextPanel := (l.focusedPanel + 1) % 3
	switch nextPanel {
	case 0:
		l.focusFileManager()
	case 1:
		l.focusEditor()
	case 2:
		l.focusTerminal()
	}
}

func (l *Layout) handleMainModeTabNavigationShortcut(event *tcell.EventKey) (*tcell.EventKey, bool) {
	if event.Key() != tcell.KeyLeft && event.Key() != tcell.KeyRight {
		return event, false
	}

	if event.Modifiers()&tcell.ModCtrl == 0 {
		return event, false
	}

	if event.Modifiers()&(tcell.ModShift|tcell.ModAlt|tcell.ModMeta) == 0 {
		return event, false
	}

	if event.Key() == tcell.KeyLeft {
		if l.focusedPanel == 1 {
			l.editor.PreviousTab()
		} else if l.focusedPanel == 2 {
			l.terminal.PreviousTab()
		}
		return nil, true
	}

	if l.focusedPanel == 1 {
		l.editor.NextTab()
	} else if l.focusedPanel == 2 {
		l.terminal.NextTab()
	}
	return nil, true
}

func (l *Layout) handleMainModeCtrlShortcuts(event *tcell.EventKey) (*tcell.EventKey, bool) {
	if event.Modifiers() != tcell.ModCtrl {
		return event, false
	}

	switch event.Key() {
	case tcell.KeyCtrlA:
		l.focusFileManager()
		return nil, true
	case tcell.KeyCtrlE:
		if l.editor.HasTabs() {
			l.focusEditor()
		}
		return nil, true
	case tcell.KeyCtrlT:
		if l.terminal.HasFocus(l.app.GetFocus()) {
			l.terminal.NewTab(l.getTerminalWorkingDir())
		} else {
			if !l.terminal.HasTabs() {
				l.terminal.NewTab(l.getTerminalWorkingDir())
				l.rebuildLayout()
			}
			l.focusTerminal()
		}
		return nil, true
	case tcell.KeyCtrlS:
		if l.focusedPanel == 1 {
			l.editor.SaveCurrentFile()
		}
		return nil, true
	case tcell.KeyCtrlF:
		if l.focusedPanel == 0 {
			l.showProjectSearch()
		} else if l.focusedPanel == 1 {
			l.editor.ShowSearch()
		}
		return nil, true
	case tcell.KeyCtrlB:
		if l.focusedPanel == 1 {
			l.showBlameModal()
		}
		return nil, true
	case tcell.KeyCtrlW:
		if l.focusedPanel == 1 {
			l.editor.CloseCurrentTab()
		} else if l.focusedPanel == 2 {
			l.terminal.CloseCurrentTab()
		}
		return nil, true
	case tcell.KeyCtrlN:
		if l.focusedPanel == 0 {
			return event, true
		}
		return nil, true
	case tcell.KeyCtrlX, tcell.KeyCtrlV, tcell.KeyCtrlD, tcell.KeyCtrlR:
		if l.focusedPanel == 0 {
			return event, true
		}
		if l.focusedPanel == 1 && (event.Key() == tcell.KeyCtrlX || event.Key() == tcell.KeyCtrlV) {
			return event, true
		}
		return nil, true
	case tcell.KeyCtrlZ, tcell.KeyCtrlY:
		return event, true
	case tcell.KeyCtrlL:
		if l.terminal.HasTabs() {
			l.cycleProportion()
		}
		return nil, true
	case tcell.KeyCtrlP:
		if l.terminal.HasTabs() {
			l.togglePosition()
		}
		return nil, true
	case tcell.KeyCtrlG:
		l.showLazygit()
		return nil, true
	case tcell.KeyCtrlQ:
		l.tryQuit()
		return nil, true
	case tcell.KeyCtrlC:
		return l.translateCtrlC(), true
	default:
		return event, false
	}
}

func (l *Layout) translateCtrlC() *tcell.EventKey {
	currentFocus := l.app.GetFocus()
	if l.focusedPanel == 0 {
		return tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModCtrl)
	}
	if l.editor.HasFocus(currentFocus) {
		return tcell.NewEventKey(tcell.KeyRune, 3, tcell.ModCtrl)
	}
	if l.terminal.HasFocus(currentFocus) {
		return tcell.NewEventKey(tcell.KeyRune, 3, tcell.ModNone)
	}
	return nil
}

func (l *Layout) setupSingleFileKeybindings() {
	l.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if IsModalOpen() {
			return event
		}

		switch event.Key() {
		case tcell.KeyF1:
			l.showHelp()
			return nil
		case tcell.KeyF8:
			ShowMemStatsModal(l.app, l.getWatcherStatsProvider())
			return nil
		case tcell.KeyF10:
			l.editor.ToggleLineWrap()
			return nil
		}

		if event.Modifiers() == tcell.ModCtrl {
			switch event.Key() {
			case tcell.KeyCtrlS:
				l.editor.SaveCurrentFile()
				return nil
			case tcell.KeyCtrlF:
				l.editor.ShowSearch()
				return nil
			case tcell.KeyCtrlQ:
				l.tryQuit()
				return nil
			case tcell.KeyCtrlZ, tcell.KeyCtrlY:
				return event
			case tcell.KeyCtrlC:
				return tcell.NewEventKey(tcell.KeyRune, 3, tcell.ModCtrl)
			}
		}

		return event
	})
}

func (l *Layout) focusFileManager() {
	if l.fileManager == nil {
		return
	}
	l.focusedPanel = 0
	view := l.fileManager.GetView()
	l.app.SetFocus(view)
	l.lastFocus = view
	l.editor.UpdateTabBar()
	if l.terminal != nil {
		l.terminal.UpdateTabBar()
	}
}

func (l *Layout) focusEditor() {
	if l.focusedPanel == 0 && l.fileManager != nil {
		l.fileManager.ClearClipboard()
	}
	l.focusedPanel = 1
	view := l.editor.focusTarget()
	l.app.SetFocus(view)
	l.lastFocus = view
	l.editor.UpdateTabBar()
	if l.terminal != nil {
		l.terminal.UpdateTabBar()
	}
}

func (l *Layout) focusTerminal() {
	if l.terminal == nil {
		return
	}
	if l.focusedPanel == 0 && l.fileManager != nil {
		l.fileManager.ClearClipboard()
	}
	l.focusedPanel = 2
	view := l.terminal.focusTarget()
	l.app.SetFocus(view)
	l.lastFocus = view
	l.editor.UpdateTabBar()
	l.terminal.UpdateTabBar()
}

// recordInode stores the inode of an open file so external renames can
// be paired later. No-op when the file can not be stated.
func (l *Layout) recordInode(path string) {
	inode, ok := fileInode(path)
	if !ok {
		return
	}
	if l.openFileInodes == nil {
		l.openFileInodes = make(map[string]uint64)
	}
	l.openFileInodes[path] = inode
}

// OpenFile opens a file in the editor and updates the layout if needed.
func (l *Layout) OpenFile(path string) {
	hadNoTabs := !l.editor.HasTabs()

	l.editor.OpenFile(path)
	l.recordInode(path)

	if l.fileWatcher != nil {
		l.fileWatcher.AddOpenFile(path)
		l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
	}

	if hadNoTabs && l.editor.HasTabs() {
		l.rebuildLayout()
	}

	l.focusEditor()
	l.saveSession()
}

// OpenFilePreview opens a file in preview mode without moving focus from file manager.
func (l *Layout) OpenFilePreview(path string) {
	hadNoTabs := !l.editor.HasTabs()

	convertedToRegular := l.editor.OpenFilePreview(path)
	l.recordInode(path)

	if l.fileWatcher != nil {
		l.fileWatcher.AddOpenFile(path)
		l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
	}

	if hadNoTabs && l.editor.HasTabs() {
		l.rebuildLayout()
	}

	if convertedToRegular {
		l.focusEditor()
		l.saveSession()
	}

	l.editor.UpdateTabBar()
}

// OpenFileAtLine opens a file at a specific line and column
func (l *Layout) OpenFileAtLine(path string, line int, column int) {
	hadNoTabs := !l.editor.HasTabs()

	l.editor.OpenFileAtLine(path, line, column)
	l.recordInode(path)

	if l.fileWatcher != nil {
		l.fileWatcher.AddOpenFile(path)
		l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
	}

	if hadNoTabs && l.editor.HasTabs() {
		l.rebuildLayout()
	}

	l.focusEditor()
	l.saveSession()
}

func (l *Layout) setupFocusDetection() {
	l.stopFocus = make(chan bool)
}

func (l *Layout) checkFocusChange() {
	currentFocus := l.app.GetFocus()

	if currentFocus == l.lastFocus {
		return
	}

	l.lastFocus = currentFocus

	oldPanel := l.focusedPanel

	if l.fileManager != nil && l.fileManager.HasFocus(currentFocus) {
		l.focusedPanel = 0
	} else if l.editor.HasFocus(currentFocus) {
		l.focusedPanel = 1
	} else if l.terminal != nil && l.terminal.HasFocus(currentFocus) {
		l.focusedPanel = 2
	}

	if oldPanel != l.focusedPanel {
		l.editor.UpdateTabBar()
		if l.terminal != nil {
			l.terminal.UpdateTabBar()
		}
	}
}

func (l *Layout) setupFileWatcher() {
	if l.singleFileMode {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	l.fileWatcher = NewFileWatcher(cwd)

	l.fileWatcher.SetOnChange(func(changes []FileChange) {
		l.app.QueueUpdateDraw(func() {
			l.handleFileChanges(changes)
		})
	})

	err = l.fileWatcher.Start()
	if err != nil {
		return
	}

	if l.fileWatcher != nil {
		l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())

		l.stopTicker = make(chan bool)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-l.stopTicker:
					return
				case <-ticker.C:
					if l.fileWatcher != nil {
						count := l.fileWatcher.GetWatchedDirsCount()
						l.app.QueueUpdateDraw(func() {
							l.statusBar.SetWatchedDirs(count)
						})
					}
				}
			}
		}()
	}
}

func (l *Layout) handleFileChanges(changes []FileChange) {
	dirsToRefresh := make(map[string]bool)

	modifiedFiles := make(map[string]bool)

	gitHeadChanged := false

	deletedFiles := make(map[string]bool)

	var renamedPaths []string
	var createdPaths []string

	for _, change := range changes {
		if strings.HasSuffix(change.Path, ".git/HEAD") {
			gitHeadChanged = true
			continue
		}

		dir := filepath.Dir(change.Path)
		dirsToRefresh[dir] = true

		switch change.Type {
		case FileModified, FileCreated:
			if change.Type == FileCreated {
				createdPaths = append(createdPaths, change.Path)
			}
			if !strings.HasSuffix(change.Path, "/") {
				modifiedFiles[change.Path] = true
			}
		case FileDeleted:
			deletedFiles[change.Path] = true
		case FileRenamed:
			renamedPaths = append(renamedPaths, change.Path)
		}
	}

	if gitHeadChanged {
		if l.statusBar != nil {
			l.statusBar.RefreshGitBranch()
		}
	}

	if l.fileManager != nil {
		for dir := range dirsToRefresh {
			l.fileManager.RefreshDirectory(dir)
		}
	}

	for _, oldPath := range renamedPaths {
		l.handleExternalRename(oldPath, createdPaths, modifiedFiles, deletedFiles)
	}

	for filePath := range modifiedFiles {
		l.editor.HandleFileModified(filePath)
	}

	for filePath := range deletedFiles {
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			l.editor.HandleFileDeleted(filePath)
		} else {
			l.editor.HandleFileModified(filePath)
		}
	}
}

// handleExternalRename reacts to a rename event for open tabs. The
// event only carries the old path, so the new one is found by pairing
// inodes against the created paths of the same batch. When the old
// path still exists it was a replace-style save, not a move. Unpaired
// renames fall back to the deleted-file flow.
func (l *Layout) handleExternalRename(oldPath string, createdPaths []string, modifiedFiles, deletedFiles map[string]bool) {
	openPaths := l.editor.GetOpenFilePaths()
	isOpen := func(path string) bool {
		for _, p := range openPaths {
			if p == path {
				return true
			}
		}
		return false
	}

	if isOpen(oldPath) {
		if _, err := os.Stat(oldPath); err == nil {
			modifiedFiles[oldPath] = true
			l.recordInode(oldPath)
			return
		}

		if inode, tracked := l.openFileInodes[oldPath]; tracked {
			for _, created := range createdPaths {
				if created == oldPath || isOpen(created) {
					continue
				}
				if ino, ok := fileInode(created); ok && ino == inode {
					l.remapOpenFile(oldPath, created)
					return
				}
			}
		}

		deletedFiles[oldPath] = true
		return
	}

	prefix := oldPath + string(filepath.Separator)
	for _, openPath := range openPaths {
		if !strings.HasPrefix(openPath, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(openPath, prefix)

		remapped := false
		for _, created := range createdPaths {
			candidate := filepath.Join(created, suffix)
			if inode, tracked := l.openFileInodes[openPath]; tracked {
				if ino, ok := fileInode(candidate); ok && ino == inode {
					l.remapOpenFile(openPath, candidate)
					remapped = true
					break
				}
			} else if _, err := os.Stat(candidate); err == nil {
				l.remapOpenFile(openPath, candidate)
				remapped = true
				break
			}
		}

		if !remapped {
			deletedFiles[openPath] = true
		}
	}
}

// remapOpenFile points an open tab to the new path of a renamed file.
func (l *Layout) remapOpenFile(oldPath, newPath string) {
	l.editor.UpdateFilePath(oldPath, newPath)

	delete(l.openFileInodes, oldPath)
	l.recordInode(newPath)

	if l.fileWatcher != nil {
		l.fileWatcher.RemoveOpenFile(oldPath)
		l.fileWatcher.AddOpenFile(newPath)
		if l.statusBar != nil {
			l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
		}
	}

	if l.claudeIntegration != nil {
		l.claudeIntegration.NotifyFileRenamed(oldPath, newPath)
	}

	l.saveSession()
}

func (l *Layout) tryQuit() {
	hasUnsaved := l.editor.HasUnsavedChanges()

	hasTerminals := l.terminal != nil && l.terminal.HasTabs()

	if hasUnsaved || hasTerminals {
		var messages []string
		if hasUnsaved {
			messages = append(messages, "You have unsaved changes in the editor.")
		}
		if hasTerminals {
			messages = append(messages, "You have open terminal sessions.")
		}
		messages = append(messages, "\n\nAre you sure you want to quit?")

		message := strings.Join(messages, " ")

		ShowConfirmModal(l.app, "Confirm Quit", message,
			func() {
				l.app.Stop()
			},
			func() {
			})
	} else {
		l.app.Stop()
	}
}

// showProjectSearch displays the project search modal
func (l *Layout) showProjectSearch() {
	if l.projectSearchModal != nil {
		l.projectSearchModal.Stop()
		l.projectSearchModal = nil
	}

	l.projectSearchModal = NewProjectSearchModal(l.app)

	l.projectSearchModal.SetCallbacks(
		func(path string, line int, column int) {
			if l.projectSearchModal != nil {
				l.projectSearchModal.Stop()
			}

			if l.projectSearchModalWrapper != nil {
				l.projectSearchModalWrapper.Close()
				l.projectSearchModalWrapper = nil
			}

			absPath, err := filepath.Abs(path)
			if err == nil {
				l.OpenFileAtLine(absPath, line, column)
			}
		},
		func() map[string]*EditorTab {
			return l.editor.GetOpenTabs()
		},
		func() {
			if l.projectSearchModal != nil {
				search, replace, caseSens, wholeWord, useRegex := l.projectSearchModal.GetState()
				l.projectSearchState.searchTerm = search
				l.projectSearchState.replaceTerm = replace
				l.projectSearchState.caseSensitive = caseSens
				l.projectSearchState.wholeWord = wholeWord
				l.projectSearchState.useRegex = useRegex

				l.projectSearchModal.Stop()
			}

			if l.projectSearchModalWrapper != nil {
				l.projectSearchModalWrapper.Close()
				l.projectSearchModalWrapper = nil
			}

			l.projectSearchModal = nil
		},
	)

	l.projectSearchModal.RestoreState(
		l.projectSearchState.searchTerm,
		l.projectSearchState.replaceTerm,
		l.projectSearchState.caseSensitive,
		l.projectSearchState.wholeWord,
		l.projectSearchState.useRegex,
	)

	l.projectSearchModal.ActivateHandlers()

	_, _, termWidth, termHeight := l.mainArea.GetRect()
	modalWidth := 80
	modalHeight := 25
	if termWidth > 190 {
		modalWidth = termWidth * 60 / 100
		modalHeight = termHeight * 60 / 100
		if modalHeight < 25 {
			modalHeight = 25
		}
	}

	config := EmModalConfig{
		UseFixedSize: true,
		FixedWidth:   modalWidth,
		FixedHeight:  modalHeight,
		BorderColor:  theme.Current.Border,
		TitleColor:   tcell.ColorWhite,
		Title:        " Project Search & Replace ",
		TitleAlign:   tview.AlignCenter,
	}

	var content *tview.Flex
	l.projectSearchModalWrapper, content = NewConfiguredModal(l.app, config, true)
	l.projectSearchModalWrapper.SetPreviousRoot(l.root)

	content.AddItem(l.projectSearchModal.GetContainer(), 0, 1, true)

	l.projectSearchModalWrapper.Show()
	l.projectSearchModal.Focus()
}

func (l *Layout) scanGitRepositories() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	l.gitRepos = git.ScanRepositories(cwd)
}

func (l *Layout) getCurrentRepo() *git.Repository {
	if len(l.gitRepos) == 0 {
		return nil
	}

	// Priority 1: Terminal CWD (if terminal is focused)
	if l.focusedPanel == 2 && l.terminal.HasTabs() {
		// GetTerminalCWD is a best-effort function, may return empty
		if termCwd := GetTerminalCWD(l.terminal.GetCurrentPID()); termCwd != "" {
			if repo := git.FindRepoForPath(termCwd, l.gitRepos); repo != nil {
				return repo
			}
		}
	}

	// Priority 2: Active editor file
	if activePath := l.editor.GetActiveFilePath(); activePath != "" {
		if repo := git.FindRepoForPath(activePath, l.gitRepos); repo != nil {
			return repo
		}
	}

	// Priority 3: File manager highlighted file
	if fmPath := l.fileManager.GetHighlightedPath(); fmPath != "" {
		if repo := git.FindRepoForPath(fmPath, l.gitRepos); repo != nil {
			return repo
		}
	}

	// Fallback: first repo
	return &l.gitRepos[0]
}

func (l *Layout) getTerminalWorkingDir() string {
	// Priority 1: Parent directory of the file currently open in the editor
	if activePath := l.editor.GetActiveFilePath(); activePath != "" {
		return filepath.Dir(activePath)
	}

	// Priority 2: Parent directory of the file highlighted in the file manager
	if fmPath := l.fileManager.GetHighlightedPath(); fmPath != "" {
		info, err := os.Stat(fmPath)
		if err == nil {
			if info.IsDir() {
				return fmPath
			}
			return filepath.Dir(fmPath)
		}
	}

	// Fallback: empty string (PTY will use project working directory)
	return ""
}

func (l *Layout) showLazygit() {
	if !git.IsLazygitInstalled() {
		ShowLazygitNotInstalledModal(l.app)
		return
	}

	if len(l.gitRepos) == 0 {
		ShowNoReposModal(l.app)
		return
	}

	currentRepo := l.getCurrentRepo()

	if len(l.gitRepos) == 1 {
		modal := NewLazygitModal(l.app, l.gitRepos[0].Path, "")
		modal.Show(nil)
		return
	}

	selector := NewRepoSelectorModal(l.app, l.gitRepos, currentRepo)
	selector.Show(func(repo git.Repository) {
		modal := NewLazygitModal(l.app, repo.Path, "")
		modal.Show(nil)
	}, nil)
}

// ShowDiffView displays a diff view as a modal overlay. If another modal
// is already open (help, confirmations, lazygit, another diff), the diff
// is queued and presented when the screen gets free, so it never steals
// the root from a live modal.
func (l *Layout) ShowDiffView(dv *DiffView) {
	if IsModalOpen() {
		l.pendingDiffs = append(l.pendingDiffs, dv)
		return
	}
	l.presentDiffView(dv)
}

func (l *Layout) presentDiffView(dv *DiffView) {
	l.activeDiffView = dv

	config := EmModalConfig{
		Width:       85,
		Height:      85,
		BorderColor: theme.Current.Border,
		TitleColor:  tcell.ColorWhite,
		Title:       " " + dv.GetTabName() + " ",
		TitleAlign:  tview.AlignCenter,
	}

	wrapper, content := NewConfiguredModal(l.app, config, true)
	wrapper.SetPreviousRoot(l.root)

	wrapper.modal.SetOnCancel(func() {
		dv.Reject()
	})
	wrapper.modal.SetCancelGuard(dv.InputAllowed)

	dv.SetBackgroundColor(theme.Current.ModalBg)
	content.AddItem(dv, 0, 1, true)

	l.diffModalWrapper = wrapper
	wrapper.Show()
	l.app.SetFocus(dv)
	dv.MarkShown()
}

func (l *Layout) showNextPendingDiff() {
	if IsModalOpen() || len(l.pendingDiffs) == 0 {
		return
	}
	next := l.pendingDiffs[0]
	l.pendingDiffs = l.pendingDiffs[1:]
	l.presentDiffView(next)
}

// CloseDiffView closes the diff modal, or removes the diff from the
// pending queue if it resolved before being shown.
func (l *Layout) CloseDiffView(dv *DiffView) {
	if l.diffModalWrapper != nil && l.activeDiffView == dv {
		wrapper := l.diffModalWrapper
		l.diffModalWrapper = nil
		l.activeDiffView = nil
		wrapper.Close()
		return
	}

	for i, pending := range l.pendingDiffs {
		if pending == dv {
			l.pendingDiffs = append(l.pendingDiffs[:i], l.pendingDiffs[i+1:]...)
			return
		}
	}
}

// SetLaunchClaudeOnStart sets whether to launch Claude Code on startup.
func (l *Layout) SetLaunchClaudeOnStart(launch bool) {
	l.launchClaudeOnStart = launch
}

// LaunchClaude opens a terminal and runs "claude -c" after shell warmup.
// If Claude Code is already connected, switches to that terminal tab instead.
func (l *Layout) LaunchClaude() {
	if l.claudeIntegration != nil && l.claudeIntegration.IsConnected() {
		if l.terminal.SwitchToClaudeTab() {
			l.focusTerminal()
			return
		}
	}

	hadNoTabs := !l.terminal.HasTabs()

	workingDir, _ := os.Getwd()
	l.terminal.NewTab(workingDir)

	if hadNoTabs {
		l.rebuildLayout()
	}

	l.focusTerminal()

	claudeTabID := l.terminal.GetLastCreatedTabID()
	l.terminal.SetClaudeTabID(claudeTabID)

	go func() {
		time.Sleep(1500 * time.Millisecond)
		l.app.QueueUpdateDraw(func() {
			l.terminal.WriteToActive("claude -c || claude\n")
		})
	}()
}

// projectRelPath converts an absolute path inside the project root to a
// slash separated relative path. Paths outside the root are rejected.
func projectRelPath(root, path string) (string, bool) {
	if !pathutil.IsWithin(root, path) {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// resolveProjectPath converts a stored relative path back to an absolute
// path, rejecting entries that escape the root or no longer exist.
func resolveProjectPath(root, rel string) (string, bool) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !pathutil.IsWithin(root, abs) {
		return "", false
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false
	}
	return abs, true
}

func (l *Layout) saveSession() {
	if l.projectStore == nil || l.restoring {
		return
	}

	root := l.projectStore.Root()
	session := project.Session{OpenFiles: []string{}}
	for _, path := range l.editor.GetPersistedFilePaths() {
		if rel, ok := projectRelPath(root, path); ok {
			session.OpenFiles = append(session.OpenFiles, rel)
		}
	}
	if active := l.editor.GetActiveFilePath(); active != "" {
		if rel, ok := projectRelPath(root, active); ok {
			for _, open := range session.OpenFiles {
				if open == rel {
					session.ActiveFile = rel
					break
				}
			}
		}
	}

	if err := l.projectStore.SaveSession(session); err != nil {
		logging.Error("Failed to save session: %v", err)
	}
}

func (l *Layout) restoreSession() {
	if l.projectStore == nil {
		return
	}

	session, err := l.projectStore.LoadSession()
	if err != nil {
		logging.Error("Failed to load session: %v", err)
		return
	}
	if len(session.OpenFiles) == 0 {
		return
	}

	root := l.projectStore.Root()

	l.restoring = true
	for _, rel := range session.OpenFiles {
		if abs, ok := resolveProjectPath(root, rel); ok {
			l.editor.OpenFile(abs)
			l.recordInode(abs)
			if l.fileWatcher != nil {
				l.fileWatcher.AddOpenFile(abs)
			}
		}
	}
	if session.ActiveFile != "" {
		if abs, ok := resolveProjectPath(root, session.ActiveFile); ok {
			l.editor.OpenFile(abs)
		}
	}
	l.restoring = false

	if l.editor.HasTabs() {
		l.rebuildLayout()
		if l.statusBar != nil && l.fileWatcher != nil {
			l.statusBar.SetWatchedDirs(l.fileWatcher.GetWatchedDirsCount())
		}
	}
}

func (l *Layout) showStarters() {
	if l.projectStore == nil {
		return
	}

	ShowStartersModal(startersModalDeps{
		app:       l.app,
		store:     l.projectStore,
		isRunning: l.isStarterRunning,
		onLaunch:  l.launchStarter,
		onJump:    l.jumpToStarterTab,
	})
}

func (l *Layout) isStarterRunning(s project.Starter) bool {
	if !s.Unique {
		return false
	}
	tabID, ok := l.starterTabs[s.Name]
	return ok && l.terminal.HasTabWithID(tabID)
}

func (l *Layout) jumpToStarterTab(s project.Starter) {
	if tabID, ok := l.starterTabs[s.Name]; ok && l.terminal.SwitchToTabByID(tabID) {
		l.focusTerminal()
	}
}

func (l *Layout) launchStarter(s project.Starter) {
	hadNoTabs := !l.terminal.HasTabs()
	previousTabID := l.terminal.GetLastCreatedTabID()

	l.terminal.NewTab(l.projectStore.Root())

	tabID := l.terminal.GetLastCreatedTabID()
	if tabID == previousTabID {
		return
	}

	if hadNoTabs {
		l.rebuildLayout()
	}

	l.focusTerminal()

	l.terminal.PinTabTitle(tabID, s.Name)

	if s.Unique {
		l.starterTabs[s.Name] = tabID
	}

	command := s.Command
	go func() {
		time.Sleep(1500 * time.Millisecond)
		l.app.QueueUpdateDraw(func() {
			l.terminal.WriteToTab(tabID, command+"\n")
		})
	}()
}

// Stop shuts down the layout and cleans up resources.
// Stop saves the session and releases every resource the layout owns: the
// Claude lock file and server, terminal processes, watchers and tickers.
// It is safe to call more than once; only the first call does the work, so
// the signal handler and the normal exit path can both call it.
func (l *Layout) Stop() {
	l.stopOnce.Do(l.stop)
}

func (l *Layout) stop() {
	l.logUnsavedChanges()
	l.saveSession()
	if l.claudeIntegration != nil {
		l.claudeIntegration.Stop()
	}
	if l.terminal != nil {
		l.terminal.StopAll()
	}
	if l.fileWatcher != nil {
		l.fileWatcher.Stop()
	}
	if l.singleFileWatcher != nil {
		l.singleFileWatcher.Stop()
	}
	if l.stopTicker != nil {
		close(l.stopTicker)
	}
	if l.stopFocus != nil {
		close(l.stopFocus)
	}
	if l.statusBar != nil {
		l.statusBar.Stop()
	}
}

// logUnsavedChanges records buffers that are lost because emed exits
// without a chance to ask, for example when the terminal hangs up.
func (l *Layout) logUnsavedChanges() {
	if l.editor == nil || !l.editor.HasUnsavedChanges() {
		return
	}
	logging.Warn("exiting with unsaved changes; modified files with a path: %v", l.editor.GetModifiedFiles())
}

func (l *Layout) setupSingleFileWatcher(path string) {
	if path == "" {
		return
	}

	watcher := NewSingleFileWatcher(path)
	watcher.SetOnChange(func(changes []FileChange) {
		l.app.QueueUpdateDraw(func() {
			l.handleFileChanges(changes)
		})
	})
	if err := watcher.Start(); err != nil {
		return
	}

	l.singleFileWatcher = watcher
}

func (l *Layout) getWatcherStatsProvider() WatcherStatsProvider {
	if l.singleFileMode {
		return l.singleFileWatcher
	}
	return l.fileWatcher
}
