// Package ui provides the main user interface components for the emed text editor.
package ui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/laborin/emed/internal/logging"
	"github.com/laborin/emed/internal/theme"
	"github.com/laborin/emed/internal/ui/components"
	"github.com/rivo/tview"
)

// TerminalTab represents a single terminal tab within the Terminal component.
type TerminalTab struct {
	id          int
	emulator    *VT10XTerminal
	modified    bool
	title       string
	titlePinned bool
	alerting    atomic.Bool
}

// IsAlerting reports if this tab received a bell while unfocused and still
// wants user attention.
func (t *TerminalTab) IsAlerting() bool {
	return t.alerting.Load()
}

// SetAlerting sets the bell attention state for this tab.
func (t *TerminalTab) SetAlerting(alerting bool) {
	t.alerting.Store(alerting)
}

// GetID returns the unique identifier for this terminal tab.
func (t *TerminalTab) GetID() int {
	return t.id
}

// GetTitle returns the display title for this terminal tab.
func (t *TerminalTab) GetTitle() string {
	title := t.title
	if title == "" {
		title = fmt.Sprintf("Term %d", t.id)
	}
	if t.emulator != nil && t.emulator.GetMode() == ModePassthrough {
		title += " *"
	}
	return title
}

// GetContent returns the tview primitive that represents the tab content.
func (t *TerminalTab) GetContent() tview.Primitive {
	return t.emulator
}

// SetModified sets the modified state of the terminal tab.
func (t *TerminalTab) SetModified(modified bool) {
	t.modified = modified
}

// IsModified returns whether the terminal tab has been modified.
func (t *TerminalTab) IsModified() bool {
	return t.modified
}

// IsPreview returns false for terminal tabs (terminals are never preview tabs).
func (t *TerminalTab) IsPreview() bool {
	return false
}

// Terminal manages multiple terminal tabs and provides terminal functionality.
type Terminal struct {
	container       *tview.Flex
	contentArea     *tview.Pages
	tabManager      *components.BaseTabManager
	noTermsView     *tview.TextView
	app             *tview.Application
	onLastTabClosed func()
	tabClickInfo    []components.TabClickInfo
	emulators       map[int]*VT10XTerminal
	claudePort      int
	claudeExecTabID int
	claudeTabID     int
	onHostBell      func()
	blinkMu         sync.Mutex
	blinkRunning    bool
}

// NewTerminal creates a new Terminal component with no tabs.
func NewTerminal() *Terminal {
	t := &Terminal{
		container:   tview.NewFlex().SetDirection(tview.FlexRow),
		contentArea: tview.NewPages(),
		tabManager:  components.NewBaseTabManager(),
		emulators:   make(map[int]*VT10XTerminal),
	}

	t.contentArea.SetBackgroundColor(theme.Current.AppBg)

	t.noTermsView = tview.NewTextView().
		SetTextAlign(tview.AlignCenter).
		SetText("\n\n\nNo terminal open.\n\nPress Ctrl+T to create a new terminal.")
	t.noTermsView.SetBackgroundColor(theme.Current.AppBg)

	addPageSafely(t.contentArea, "no-terms", t.noTermsView, true, false, t.app)

	t.container.SetBackgroundColor(theme.Current.AppBg)
	t.container.
		AddItem(t.contentArea, 0, 1, true).
		SetBorder(true).
		SetBorderColor(theme.Current.Border).
		SetTitle(" Terminal ").
		SetTitleColor(theme.Current.Title).
		SetTitleAlign(tview.AlignLeft)

	t.container.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			x, y := event.Position()
			containerX, containerY, containerWidth, _ := t.container.GetRect()

			if y == containerY && x >= containerX && x < containerX+containerWidth {
				relativeX := x - containerX - 1

				for _, clickInfo := range t.tabClickInfo {
					if relativeX >= clickInfo.StartX && relativeX < clickInfo.EndX {
						tabs := t.tabManager.GetTabs()
						if clickInfo.TabIndex < len(tabs) {
							tabID := tabs[clickInfo.TabIndex].GetID()
							t.tabManager.SwitchToTab(tabID)
							t.switchToTabByID(tabID)
							if t.app != nil {
								t.app.SetFocus(t.focusTarget())
							}
						}
						return action, nil
					}
				}
			}
		}
		return action, event
	})

	t.showNoTermsView()

	t.tabManager.SetOnLastTabClosed(func() {
		t.showNoTermsView()
		if t.onLastTabClosed != nil {
			t.onLastTabClosed()
		}
	})

	t.tabManager.SetOnTabClosed(func(id int) {
		if emulator, ok := t.emulators[id]; ok {
			emulator.Stop()
			delete(t.emulators, id)
		}
		removePageSafely(t.contentArea, fmt.Sprintf("term-%d", id), t.app)
		t.UpdateTabBar()
	})

	return t
}

// SetApp sets the application reference for the terminal.
func (t *Terminal) SetApp(app *tview.Application) {
	t.app = app
}

// NewTab creates and adds a new terminal tab with the specified working directory.
func (t *Terminal) NewTab(workingDir string) {
	emulator := NewVT10XTerminal(workingDir)

	if t.claudePort > 0 {
		emulator.SetExtraEnv([]string{
			fmt.Sprintf("CLAUDE_CODE_SSE_PORT=%d", t.claudePort),
			"ENABLE_IDE_INTEGRATION=true",
		})
	}

	if t.app != nil {
		emulator.SetApp(t.app)
	}

	tabID := t.tabManager.GetNextTabID()

	emulator.SetOnModeChange(func(mode TerminalMode) {
		t.UpdateTabBar()
	})

	emulator.SetOnExit(func() {
		if t.app != nil {
			t.app.QueueUpdateDraw(func() {
				t.tabManager.RemoveTab(tabID)
			})
		}
	})

	emulator.SetOnTitleChange(func(title string) {
		if t.app != nil {
			t.app.QueueUpdateDraw(func() {
				tabs := t.tabManager.GetTabs()
				for _, tab := range tabs {
					if tab.GetID() == tabID {
						if termTab, ok := tab.(*TerminalTab); ok && !termTab.titlePinned {
							termTab.title = title
							t.UpdateTabBar()
						}
						break
					}
				}
			})
		}
	})

	emulator.SetOnBell(func() {
		t.handleBell(tabID)
	})

	if err := emulator.Start(); err != nil {
		errorView := tview.NewTextView().
			SetText(fmt.Sprintf("Failed to start terminal: %v", err)).
			SetTextColor(tcell.ColorRed)
		addPageSafely(t.contentArea, fmt.Sprintf("term-%d", tabID), errorView, true, false, t.app)
		return
	}

	t.emulators[tabID] = emulator

	tab := &TerminalTab{
		id:       tabID,
		emulator: emulator,
	}

	addPageSafely(t.contentArea, fmt.Sprintf("term-%d", tabID), emulator, true, false, t.app)

	t.tabManager.AddTab(tab)
	t.switchToTabByID(tabID)
}

func (t *Terminal) showNoTermsView() {
	switchToPageSafely(t.contentArea, "no-terms", t.app)
	t.container.SetTitle(" Terminal ")
}

func (t *Terminal) switchToTabByID(id int) {
	switchToPageSafely(t.contentArea, fmt.Sprintf("term-%d", id), t.app)
	t.UpdateTabBar()
	if t.app != nil && t.HasFocus(t.app.GetFocus()) {
		t.app.SetFocus(t.focusTarget())
	}
}

func (t *Terminal) focusTarget() tview.Primitive {
	activeTab := t.tabManager.GetActiveTab()
	if termTab, ok := activeTab.(*TerminalTab); ok && termTab.emulator != nil {
		return termTab.emulator
	}
	if t.noTermsView != nil {
		return t.noTermsView
	}
	return t.container
}

func (t *Terminal) switchToTabByIndex(index int) {
	tabs := t.tabManager.GetTabs()
	if index >= 0 && index < len(tabs) {
		t.switchToTabByID(tabs[index].GetID())
	}
}

// SetOnHostBell sets a callback used to forward a terminal bell to the
// host terminal where emed is running.
func (t *Terminal) SetOnHostBell(handler func()) {
	t.onHostBell = handler
}

func (t *Terminal) handleBell(tabID int) {
	if t.onHostBell != nil {
		t.onHostBell()
	}

	if t.app == nil {
		return
	}

	t.app.QueueUpdateDraw(func() {
		var termTab *TerminalTab
		for _, tab := range t.tabManager.GetTabs() {
			if tab.GetID() == tabID {
				termTab, _ = tab.(*TerminalTab)
				break
			}
		}
		if termTab == nil {
			return
		}

		if t.isTabFocused(termTab) {
			return
		}

		termTab.SetAlerting(true)
		t.UpdateTabBar()
		t.ensureBlinkLoop()
	})
}

func (t *Terminal) isTabFocused(tab *TerminalTab) bool {
	if t.app == nil || !t.HasFocus(t.app.GetFocus()) {
		return false
	}
	active := t.tabManager.GetActiveTab()
	return active != nil && active.GetID() == tab.GetID()
}

func (t *Terminal) clearAlertOnFocusedTab() {
	if t.app == nil || !t.HasFocus(t.app.GetFocus()) {
		return
	}
	if termTab, ok := t.tabManager.GetActiveTab().(*TerminalTab); ok {
		termTab.SetAlerting(false)
	}
}

func (t *Terminal) hasAlertingTabs() bool {
	for _, tab := range t.tabManager.GetTabs() {
		if termTab, ok := tab.(*TerminalTab); ok && termTab.IsAlerting() {
			return true
		}
	}
	return false
}

func (t *Terminal) ensureBlinkLoop() {
	t.blinkMu.Lock()
	defer t.blinkMu.Unlock()
	if t.blinkRunning {
		return
	}
	t.blinkRunning = true
	go t.blinkLoop()
}

func (t *Terminal) blinkLoop() {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("blinkLoop panic: %v", r)
		}
	}()

	ticker := time.NewTicker(components.TabAlertBlinkInterval)
	defer ticker.Stop()

	for range ticker.C {
		alerting := false
		t.app.QueueUpdateDraw(func() {
			t.UpdateTabBar()
			alerting = t.hasAlertingTabs()
		})
		if !alerting {
			t.blinkMu.Lock()
			t.blinkRunning = false
			t.blinkMu.Unlock()
			return
		}
	}
}

// UpdateTabBar refreshes the tab bar display with current tabs.
func (t *Terminal) UpdateTabBar() {
	t.clearAlertOnFocusedTab()

	if !t.tabManager.HasTabs() {
		t.container.SetTitle(" Terminal ").SetTitleColor(theme.Current.Title)
		return
	}

	_, _, width, _ := t.container.GetRect()
	availableWidth := width - 5
	if availableWidth < 0 {
		availableWidth = 0
	}

	hasFocus := false
	if t.app != nil {
		hasFocus = t.HasFocus(t.app.GetFocus())
	}
	tabBarText, clickInfo := t.tabManager.GenerateTabBarTextWithPositions(availableWidth, hasFocus)
	t.tabClickInfo = clickInfo
	if tabBarText != "" {
		title := fmt.Sprintf(" %s ", tabBarText)
		t.container.SetTitle(title).SetTitleColor(theme.Current.Title)
	} else {
		t.container.SetTitle(" Terminal ").SetTitleColor(theme.Current.Title)
	}
}

// NextTab switches to the next terminal tab.
func (t *Terminal) NextTab() {
	tabID := t.tabManager.NextTabWithID()
	if tabID != -1 {
		t.switchToTabByID(tabID)
	}
}

// PreviousTab switches to the previous terminal tab.
func (t *Terminal) PreviousTab() {
	tabID := t.tabManager.PreviousTabWithID()
	if tabID != -1 {
		t.switchToTabByID(tabID)
	}
}

// CloseCurrentTab closes the currently active terminal tab.
func (t *Terminal) CloseCurrentTab() {
	activeTab := t.tabManager.GetActiveTab()
	if activeTab != nil {
		if termTab, ok := activeTab.(*TerminalTab); ok && termTab.emulator != nil {
			termTab.emulator.Stop()
		}
		t.tabManager.RemoveTab(activeTab.GetID())
	}
}

// StopAll terminates every terminal process. Used on shutdown, so the shells
// and the programs they run do not outlive emed.
func (t *Terminal) StopAll() {
	for id, emulator := range t.emulators {
		emulator.Stop()
		delete(t.emulators, id)
	}
}

// GetView returns the root primitive for the terminal component.
func (t *Terminal) GetView() tview.Primitive {
	return t.container
}

// HasFocus checks if the given primitive is part of the terminal component.
func (t *Terminal) HasFocus(focused tview.Primitive) bool {
	if focused == t.container || focused == t.contentArea {
		return true
	}
	tabs := t.tabManager.GetTabs()
	for _, tab := range tabs {
		if focused == tab.GetContent() {
			return true
		}
	}
	if focused == t.noTermsView {
		return true
	}
	return false
}

// HasTabs returns true if there are any open terminal tabs.
func (t *Terminal) HasTabs() bool {
	return t.tabManager.HasTabs()
}

// SetOnLastTabClosed sets a callback for when the last terminal tab is closed.
func (t *Terminal) SetOnLastTabClosed(handler func()) {
	t.onLastTabClosed = handler
}

// IsInPassthroughMode returns true if the active terminal is in passthrough mode.
func (t *Terminal) IsInPassthroughMode() bool {
	activeTab := t.tabManager.GetActiveTab()
	if termTab, ok := activeTab.(*TerminalTab); ok && termTab.emulator != nil {
		return termTab.emulator.GetMode() == ModePassthrough
	}
	return false
}

// GetCurrentPID returns the process ID of the active terminal, or 0 if none.
func (t *Terminal) GetCurrentPID() int {
	activeTab := t.tabManager.GetActiveTab()
	if termTab, ok := activeTab.(*TerminalTab); ok && termTab.emulator != nil {
		return termTab.emulator.GetPID()
	}
	return 0
}

// SetClaudePort sets the Claude Code server port for environment injection.
func (t *Terminal) SetClaudePort(port int) {
	t.claudePort = port
}

// WriteToActive writes data to the active terminal.
func (t *Terminal) WriteToActive(data string) {
	activeTab := t.tabManager.GetActiveTab()
	if termTab, ok := activeTab.(*TerminalTab); ok && termTab.emulator != nil {
		termTab.emulator.Write([]byte(data))
	}
}

// EnsureClaudeExecTerminal creates or ensures a dedicated terminal exists for Claude code execution.
func (t *Terminal) EnsureClaudeExecTerminal(workingDir string) {
	if t.claudeExecTabID != 0 {
		tabs := t.tabManager.GetTabs()
		for _, tab := range tabs {
			if tab.GetID() == t.claudeExecTabID {
				return
			}
		}
		t.claudeExecTabID = 0
	}

	emulator := NewVT10XTerminal(workingDir)

	if t.app != nil {
		emulator.SetApp(t.app)
	}

	tabID := t.tabManager.GetNextTabID()
	t.claudeExecTabID = tabID

	emulator.SetOnModeChange(func(mode TerminalMode) {
		t.UpdateTabBar()
	})

	emulator.SetOnExit(func() {
		if t.app != nil {
			t.app.QueueUpdateDraw(func() {
				if t.claudeExecTabID == tabID {
					t.claudeExecTabID = 0
				}
				t.tabManager.RemoveTab(tabID)
			})
		}
	})

	emulator.SetOnTitleChange(func(title string) {
		if t.app != nil {
			t.app.QueueUpdateDraw(func() {
				tabs := t.tabManager.GetTabs()
				for _, tab := range tabs {
					if tab.GetID() == tabID {
						if termTab, ok := tab.(*TerminalTab); ok {
							termTab.title = title
							t.UpdateTabBar()
						}
						break
					}
				}
			})
		}
	})

	emulator.SetOnBell(func() {
		t.handleBell(tabID)
	})

	if err := emulator.Start(); err != nil {
		errorView := tview.NewTextView().
			SetText(fmt.Sprintf("Failed to start Claude exec terminal: %v", err)).
			SetTextColor(tcell.ColorRed)
		addPageSafely(t.contentArea, fmt.Sprintf("term-%d", tabID), errorView, true, false, t.app)
		t.claudeExecTabID = 0
		return
	}

	t.emulators[tabID] = emulator

	tab := &TerminalTab{
		id:       tabID,
		emulator: emulator,
		title:    "Claude Exec",
	}

	addPageSafely(t.contentArea, fmt.Sprintf("term-%d", tabID), emulator, true, false, t.app)

	t.tabManager.AddTab(tab)
	t.UpdateTabBar()
}

// WriteToClaudeExec writes data to the dedicated Claude execution terminal.
func (t *Terminal) WriteToClaudeExec(data string) {
	if t.claudeExecTabID == 0 {
		return
	}

	if emulator, ok := t.emulators[t.claudeExecTabID]; ok {
		emulator.Write([]byte(data))
	}
}

// SetClaudeTabID sets the terminal tab ID where Claude Code is running.
func (t *Terminal) SetClaudeTabID(id int) {
	t.claudeTabID = id
}

// GetClaudeTabID returns the terminal tab ID where Claude Code is running.
func (t *Terminal) GetClaudeTabID() int {
	return t.claudeTabID
}

// SwitchToClaudeTab switches to the Claude Code terminal tab if it exists.
func (t *Terminal) SwitchToClaudeTab() bool {
	if t.claudeTabID == 0 {
		return false
	}

	tabs := t.tabManager.GetTabs()
	for _, tab := range tabs {
		if tab.GetID() == t.claudeTabID {
			t.tabManager.SwitchToTab(t.claudeTabID)
			t.switchToTabByID(t.claudeTabID)
			return true
		}
	}

	t.claudeTabID = 0
	return false
}

// PinTabTitle sets a fixed title on a tab, ignoring future title
// changes coming from the shell.
func (t *Terminal) PinTabTitle(id int, title string) {
	for _, tab := range t.tabManager.GetTabs() {
		if tab.GetID() == id {
			if termTab, ok := tab.(*TerminalTab); ok {
				termTab.title = title
				termTab.titlePinned = true
				t.UpdateTabBar()
			}
			return
		}
	}
}

// HasTabWithID reports whether a terminal tab with the given ID exists.
func (t *Terminal) HasTabWithID(id int) bool {
	if id == 0 {
		return false
	}
	for _, tab := range t.tabManager.GetTabs() {
		if tab.GetID() == id {
			return true
		}
	}
	return false
}

// SwitchToTabByID switches to the tab with the given ID if it exists.
func (t *Terminal) SwitchToTabByID(id int) bool {
	if !t.HasTabWithID(id) {
		return false
	}
	t.tabManager.SwitchToTab(id)
	t.switchToTabByID(id)
	return true
}

// WriteToTab writes data to a specific terminal tab.
func (t *Terminal) WriteToTab(id int, data string) {
	if emulator, ok := t.emulators[id]; ok {
		emulator.Write([]byte(data))
	}
}

// GetActiveTabID returns the ID of the currently active terminal tab.
func (t *Terminal) GetActiveTabID() int {
	activeTab := t.tabManager.GetActiveTab()
	if activeTab != nil {
		return activeTab.GetID()
	}
	return 0
}

// GetLastCreatedTabID returns the ID of the most recently created tab.
func (t *Terminal) GetLastCreatedTabID() int {
	tabs := t.tabManager.GetTabs()
	if len(tabs) == 0 {
		return 0
	}
	return tabs[len(tabs)-1].GetID()
}
