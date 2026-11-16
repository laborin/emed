// Package ui provides the main user interface components for the emed text editor.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/laborin/emed/internal/logging"
	"github.com/laborin/emed/internal/theme"
	"github.com/laborin/emed/internal/ui/components"
	"github.com/rivo/tview"
)

// EditorTab represents a single file tab within the Editor component.
type EditorTab struct {
	id                 int
	path               string
	modified           bool
	isPreview          bool
	codeEditor         *CodeEditor
	externallyModified bool
	lastModTime        time.Time
	searchBar          *SearchBar
	searchVisible      bool
	container          *tview.Flex
}

// GetID returns the unique identifier for this editor tab.
func (t *EditorTab) GetID() int {
	return t.id
}

// GetTitle returns the display title for this editor tab.
func (t *EditorTab) GetTitle() string {
	if t.path == "" {
		return "Untitled"
	}
	return filepath.Base(t.path)
}

// GetContent returns the tview primitive that represents the tab content.
func (t *EditorTab) GetContent() tview.Primitive {
	return t.container
}

// SetModified sets the modified state of the editor tab.
func (t *EditorTab) SetModified(modified bool) {
	t.modified = modified
}

// IsModified returns whether the editor tab has unsaved changes.
func (t *EditorTab) IsModified() bool {
	return t.modified
}

// IsPreview returns whether this is a preview tab.
func (t *EditorTab) IsPreview() bool {
	return t.isPreview
}

// GetPath returns the file path of the editor tab.
func (t *EditorTab) GetPath() string {
	return t.path
}

// IsDirty returns whether the tab has unsaved changes.
func (t *EditorTab) IsDirty() bool {
	return t.modified
}

// GetCodeEditor returns the underlying code editor.
func (t *EditorTab) GetCodeEditor() *CodeEditor {
	return t.codeEditor
}

// Editor manages multiple file tabs and provides text editing functionality.
type Editor struct {
	container           *tview.Flex
	contentArea         *tview.Pages
	tabManager          *components.BaseTabManager
	tabs                []*EditorTab
	tabsByID            map[int]*EditorTab
	tabsByPath          map[string]*EditorTab
	tabsMu              sync.RWMutex
	noDocsView          tview.Primitive
	app                 *tview.Application
	onLastTabClosed     func()
	onTabClosed         func(path string)
	onActiveFileChanged func(path string)
	onSelectionChange   func(path string, text string, startRow, startCol, endRow, endCol int)
	onFileSaved         func(path string)
	fileChangeHandler   *components.FileChangeHandler
	dialogManager       *EditorDialogManager
	tabClickInfo        []components.TabClickInfo
	singleFileMode      bool
	lineWrapEnabled     bool
}

// NewEditor creates a new Editor component with no open files.
func NewEditor() *Editor {
	e := &Editor{
		container:         tview.NewFlex().SetDirection(tview.FlexRow),
		contentArea:       tview.NewPages(),
		tabManager:        components.NewBaseTabManager(),
		tabs:              make([]*EditorTab, 0),
		tabsByID:          make(map[int]*EditorTab),
		tabsByPath:        make(map[string]*EditorTab),
		fileChangeHandler: components.NewFileChangeHandler(),
		dialogManager:     nil,
		lineWrapEnabled:   defaultLineWrapEnabled,
	}

	e.contentArea.SetBackgroundColor(theme.Current.AppBg)

	splashScreen := components.NewSplashScreen()
	e.noDocsView = splashScreen.CreateNoDocsView()

	addPageSafely(e.contentArea, "no-docs", e.noDocsView, true, false, e.app)

	e.container.SetBackgroundColor(theme.Current.AppBg)
	e.container.
		AddItem(e.contentArea, 0, 1, true).
		SetBorder(true).
		SetBorderColor(theme.Current.Border).
		SetTitle(" Editor ").
		SetTitleColor(theme.Current.Title).
		SetTitleAlign(tview.AlignLeft)

	e.container.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			x, y := event.Position()
			containerX, containerY, containerWidth, _ := e.container.GetRect()

			if y == containerY && x >= containerX && x < containerX+containerWidth {
				relativeX := x - containerX - 1

				for _, clickInfo := range e.tabClickInfo {
					if relativeX >= clickInfo.StartX && relativeX < clickInfo.EndX {
						tabs := e.tabManager.GetTabs()
						if clickInfo.TabIndex < len(tabs) {
							tabID := tabs[clickInfo.TabIndex].GetID()
							e.tabManager.SwitchToTab(tabID)
							e.switchToTabByID(tabID)
							if e.app != nil {
								e.app.SetFocus(e.focusTarget())
							}
						}
						return action, nil
					}
				}
			}
		}
		return action, event
	})

	e.showNoDocsView()

	e.tabManager.SetOnLastTabClosed(func() {
		e.showNoDocsView()
		if e.onLastTabClosed != nil {
			e.onLastTabClosed()
		}
	})

	e.tabManager.SetOnTabClosed(func(id int) {
		e.tabsMu.Lock()
		var tab *EditorTab
		var path string
		if current, ok := e.tabsByID[id]; ok {
			tab = current
			path = current.path
			delete(e.tabsByID, id)
			if path != "" {
				delete(e.tabsByPath, path)
			}
			for i, t := range e.tabs {
				if t.id == id {
					e.tabs[i] = nil
					e.tabs = append(e.tabs[:i], e.tabs[i+1:]...)
					break
				}
			}
		}
		e.tabsMu.Unlock()

		removePageSafely(e.contentArea, fmt.Sprintf("editor-%d", id), e.app)
		e.releaseTabResources(tab)
		if e.onTabClosed != nil && path != "" {
			e.onTabClosed(path)
		}
		e.UpdateTabBar()
	})

	return e
}

func (e *Editor) showNoDocsView() {
	switchToPageSafely(e.contentArea, "no-docs", e.app)
	e.UpdateTabBar()
}

func (e *Editor) switchToTabByID(id int) {
	e.tabsMu.RLock()
	tab, found := e.tabsByID[id]
	var filePath string
	if found {
		filePath = tab.path
	}
	e.tabsMu.RUnlock()

	if found {
		switchToPageSafely(e.contentArea, fmt.Sprintf("editor-%d", id), e.app)
		e.UpdateTabBar()
		if e.app != nil && e.HasFocus(e.app.GetFocus()) {
			e.app.SetFocus(e.focusTarget())
		}
		if e.onActiveFileChanged != nil && filePath != "" {
			e.onActiveFileChanged(filePath)
		}
	}
}

func (e *Editor) focusTarget() tview.Primitive {
	activeTab := e.tabManager.GetActiveTab()
	if tab, ok := activeTab.(*EditorTab); ok {
		if tab.searchVisible && tab.searchBar != nil {
			return tab.searchBar
		}
		if tab.codeEditor != nil {
			return tab.codeEditor
		}
	}
	if e.noDocsView != nil {
		return e.noDocsView
	}
	return e.container
}

func (e *Editor) switchToTabByIndex(index int) {
	tabs := e.tabManager.GetTabs()
	if index >= 0 && index < len(tabs) {
		e.switchToTabByID(tabs[index].GetID())
	}
}

// OpenFile opens a file in a new tab or switches to existing tab.
func (e *Editor) OpenFile(path string) {
	if e.singleFileMode {
		e.tabsMu.RLock()
		if len(e.tabs) > 0 {
			current := e.tabs[0]
			e.tabsMu.RUnlock()
			if current.path == path {
				e.tabManager.SwitchToTab(current.id)
				e.switchToTabByID(current.id)
			}
			return
		}
		e.tabsMu.RUnlock()
	}

	e.tabsMu.RLock()
	existingTab, exists := e.tabsByPath[path]
	e.tabsMu.RUnlock()

	if exists {
		e.tabManager.SwitchToTab(existingTab.id)
		e.switchToTabByID(existingTab.id)
		return
	}

	codeEditor := NewCodeEditor()
	codeEditor.SetLineWrapEnabled(e.lineWrapEnabled)
	if e.app != nil {
		codeEditor.SetApp(e.app)
	}

	err := codeEditor.LoadFile(path)
	if err != nil {
		return
	}

	tabID := e.tabManager.GetNextTabID()

	codeEditor.SetOnModified(func(modified bool) {
		e.tabsMu.RLock()
		for _, t := range e.tabs {
			if t.codeEditor == codeEditor {
				t.SetModified(modified)
				e.tabsMu.RUnlock()
				e.UpdateTabBar()
				return
			}
		}
		e.tabsMu.RUnlock()
	})

	filePath := path
	codeEditor.SetOnSelectionChange(func() {
		if e.onSelectionChange == nil {
			return
		}
		text, startRow, startCol, endRow, endCol, active := codeEditor.GetSelectionInfo()
		if active {
			e.onSelectionChange(filePath, text, startRow, startCol, endRow, endCol)
		}
	})

	var lastModTime time.Time
	if info, err := os.Stat(path); err == nil {
		lastModTime = info.ModTime()
	}

	searchBar := NewSearchBar()
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	container.AddItem(codeEditor, 0, 1, true)

	tab := &EditorTab{
		id:                 tabID,
		path:               path,
		modified:           false,
		codeEditor:         codeEditor,
		externallyModified: false,
		lastModTime:        lastModTime,
		searchBar:          searchBar,
		searchVisible:      false,
		container:          container,
	}

	e.setupSearchBar(tab)

	e.tabsMu.Lock()
	e.tabs = append(e.tabs, tab)
	e.tabsByID[tab.id] = tab
	if tab.path != "" {
		e.tabsByPath[tab.path] = tab
	}
	e.tabsMu.Unlock()
	addPageSafely(e.contentArea, fmt.Sprintf("editor-%d", tabID), container, true, false, e.app)

	e.tabManager.AddTab(tab)
	e.switchToTabByID(tabID)
}

// OpenFileAtLine opens a file and jumps to a specific line and column
func (e *Editor) OpenFileAtLine(path string, line int, column int) {
	e.OpenFile(path)

	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return
	}

	if line > 0 {
		row := line - 1
		col := column
		if col < 0 {
			col = 0
		}
		tab.codeEditor.SetCursor(row, col)
		tab.codeEditor.RequestCenterCursor()
	}
}

// GetPreviewTabPath returns the path of the current preview tab, or empty string if none.
func (e *Editor) GetPreviewTabPath() string {
	e.tabsMu.RLock()
	defer e.tabsMu.RUnlock()
	for _, tab := range e.tabs {
		if tab.isPreview {
			return tab.path
		}
	}
	return ""
}

// closePreviewTab closes the current preview tab if one exists.
// This removes the tab silently without triggering onLastTabClosed callback
// since we're about to add a new preview tab immediately after.
func (e *Editor) closePreviewTab() {
	e.tabsMu.Lock()
	var previewTabID int = -1
	var previewTabPath string
	var previewTab *EditorTab
	for i, tab := range e.tabs {
		if tab.isPreview {
			previewTabID = tab.id
			previewTabPath = tab.path
			previewTab = tab
			delete(e.tabsByID, tab.id)
			if tab.path != "" {
				delete(e.tabsByPath, tab.path)
			}
			e.tabs[i] = nil
			e.tabs = append(e.tabs[:i], e.tabs[i+1:]...)
			break
		}
	}
	e.tabsMu.Unlock()

	if previewTabID != -1 {
		removePageSafely(e.contentArea, fmt.Sprintf("editor-%d", previewTabID), e.app)
		e.releaseTabResources(previewTab)
		e.tabManager.RemoveTabSilently(previewTabID)
		if e.onTabClosed != nil && previewTabPath != "" {
			e.onTabClosed(previewTabPath)
		}
	}
}

// OpenFilePreview opens a file in preview mode. Preview tabs are replaced when
// opening another file, and convert to regular tabs on edit or second open.
// Returns true if a preview tab was converted to regular (second click).
func (e *Editor) OpenFilePreview(path string) bool {
	if e.singleFileMode {
		e.OpenFile(path)
		return false
	}

	e.tabsMu.RLock()
	existingTab, exists := e.tabsByPath[path]
	e.tabsMu.RUnlock()

	if exists {
		if existingTab.isPreview {
			e.ConvertPreviewToRegular(path)
			return true
		} else {
			e.tabManager.SwitchToTab(existingTab.id)
			e.switchToTabByID(existingTab.id)
		}
		return false
	}

	e.closePreviewTab()

	codeEditor := NewCodeEditor()
	codeEditor.SetLineWrapEnabled(e.lineWrapEnabled)
	if e.app != nil {
		codeEditor.SetApp(e.app)
	}

	err := codeEditor.LoadFile(path)
	if err != nil {
		return false
	}

	tabID := e.tabManager.GetNextTabID()

	codeEditor.SetOnModified(func(modified bool) {
		e.tabsMu.Lock()
		for _, t := range e.tabs {
			if t.codeEditor == codeEditor {
				t.SetModified(modified)
				if modified && t.isPreview {
					t.isPreview = false
				}
				e.tabsMu.Unlock()
				e.UpdateTabBar()
				return
			}
		}
		e.tabsMu.Unlock()
	})

	filePath := path
	codeEditor.SetOnSelectionChange(func() {
		if e.onSelectionChange == nil {
			return
		}
		text, startRow, startCol, endRow, endCol, active := codeEditor.GetSelectionInfo()
		if active {
			e.onSelectionChange(filePath, text, startRow, startCol, endRow, endCol)
		}
	})

	var lastModTime time.Time
	if info, err := os.Stat(path); err == nil {
		lastModTime = info.ModTime()
	}

	searchBar := NewSearchBar()
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	container.AddItem(codeEditor, 0, 1, true)

	tab := &EditorTab{
		id:                 tabID,
		path:               path,
		modified:           false,
		isPreview:          true,
		codeEditor:         codeEditor,
		externallyModified: false,
		lastModTime:        lastModTime,
		searchBar:          searchBar,
		searchVisible:      false,
		container:          container,
	}

	e.setupSearchBar(tab)

	e.tabsMu.Lock()
	e.tabs = append(e.tabs, tab)
	e.tabsByID[tab.id] = tab
	if tab.path != "" {
		e.tabsByPath[tab.path] = tab
	}
	e.tabsMu.Unlock()
	addPageSafely(e.contentArea, fmt.Sprintf("editor-%d", tabID), container, true, false, e.app)

	e.tabManager.AddTab(tab)
	e.switchToTabByID(tabID)
	return false
}

// ConvertPreviewToRegular converts a preview tab to a regular tab.
func (e *Editor) ConvertPreviewToRegular(path string) {
	e.tabsMu.Lock()
	if tab, ok := e.tabsByPath[path]; ok && tab.isPreview {
		tab.isPreview = false
		e.tabsMu.Unlock()
		e.UpdateTabBar()
		return
	}
	e.tabsMu.Unlock()
}

// NewDocument creates a new untitled document tab.
func (e *Editor) NewDocument() {
	if e.singleFileMode {
		return
	}

	codeEditor := NewCodeEditor()
	codeEditor.SetLineWrapEnabled(e.lineWrapEnabled)
	if e.app != nil {
		codeEditor.SetApp(e.app)
	}

	tabID := e.tabManager.GetNextTabID()

	codeEditor.SetOnModified(func(modified bool) {
		e.tabsMu.RLock()
		for _, t := range e.tabs {
			if t.codeEditor == codeEditor {
				t.SetModified(modified)
				e.tabsMu.RUnlock()
				e.UpdateTabBar()
				return
			}
		}
		e.tabsMu.RUnlock()
	})

	codeEditor.SetOnSelectionChange(func() {
		if e.onSelectionChange == nil {
			return
		}
		text, startRow, startCol, endRow, endCol, active := codeEditor.GetSelectionInfo()
		if active {
			e.onSelectionChange("", text, startRow, startCol, endRow, endCol)
		}
	})

	searchBar := NewSearchBar()
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	container.AddItem(codeEditor, 0, 1, true)

	tab := &EditorTab{
		id:                 tabID,
		path:               "",
		modified:           false,
		codeEditor:         codeEditor,
		externallyModified: false,
		lastModTime:        time.Time{},
		searchBar:          searchBar,
		searchVisible:      false,
		container:          container,
	}

	e.setupSearchBar(tab)

	e.tabsMu.Lock()
	e.tabs = append(e.tabs, tab)
	e.tabsByID[tab.id] = tab
	e.tabsMu.Unlock()
	addPageSafely(e.contentArea, fmt.Sprintf("editor-%d", tabID), container, true, false, e.app)

	e.tabManager.AddTab(tab)
	e.switchToTabByID(tabID)
}

// ShowOpenDialog displays the file open dialog.
func (e *Editor) ShowOpenDialog() {
	// TODO: Implement file open dialog
}

func (e *Editor) switchToTab(index int) {
	if index < 0 || index >= len(e.tabs) {
		return
	}

	tabID := e.tabManager.GoToTabByIndexWithID(index)
	if tabID != -1 {
		e.switchToTabByID(tabID)
	}
}

// UpdateTabBar refreshes the tab bar display with current tabs.
func (e *Editor) UpdateTabBar() {
	if e.singleFileMode {
		e.updateSingleFileTitle()
		return
	}

	if !e.tabManager.HasTabs() {
		e.container.SetTitle(" Editor ").SetTitleColor(theme.Current.Title)
		return
	}

	_, _, width, _ := e.container.GetRect()
	availableWidth := width - 5
	if availableWidth < 0 {
		availableWidth = 0
	}

	hasFocus := false
	if e.app != nil {
		hasFocus = e.HasFocus(e.app.GetFocus())
	}
	tabBarText, clickInfo := e.tabManager.GenerateTabBarTextWithPositions(availableWidth, hasFocus)
	e.tabClickInfo = clickInfo
	if tabBarText != "" {
		title := fmt.Sprintf(" %s ", tabBarText)
		e.container.SetTitle(title).SetTitleColor(theme.Current.Title)
	} else {
		e.container.SetTitle(" Editor ").SetTitleColor(theme.Current.Title)
	}
}

func (e *Editor) updateSingleFileTitle() {
	title := " EMED - F1 for Help "

	tab := e.getCurrentTab()
	if tab != nil {
		title = fmt.Sprintf(" [%s] - EMED - F1 for Help ", tab.GetTitle())
	}

	e.tabClickInfo = nil
	e.container.SetTitle(title).SetTitleColor(theme.Current.Title)
}

// NextTab switches to the next editor tab.
func (e *Editor) NextTab() {
	tabID := e.tabManager.NextTabWithID()
	if tabID != -1 {
		e.switchToTabByID(tabID)
	}
}

// PreviousTab switches to the previous editor tab.
func (e *Editor) PreviousTab() {
	tabID := e.tabManager.PreviousTabWithID()
	if tabID != -1 {
		e.switchToTabByID(tabID)
	}
}

// ToggleLineWrap toggles soft line wrapping for all editor tabs.
func (e *Editor) ToggleLineWrap() bool {
	e.lineWrapEnabled = !e.lineWrapEnabled

	e.tabsMu.RLock()
	for _, tab := range e.tabs {
		if tab.codeEditor != nil {
			tab.codeEditor.SetLineWrapEnabled(e.lineWrapEnabled)
		}
	}
	e.tabsMu.RUnlock()

	return e.lineWrapEnabled
}

// LineWrapEnabled returns whether new editor tabs use soft line wrapping.
func (e *Editor) LineWrapEnabled() bool {
	return e.lineWrapEnabled
}

// CloseCurrentTab closes the currently active editor tab.
func (e *Editor) CloseCurrentTab() {
	if e.singleFileMode {
		return
	}

	activeID := e.tabManager.GetActiveTabID()
	if activeID < 0 {
		return
	}

	e.tabsMu.RLock()
	var tab *EditorTab
	for _, t := range e.tabs {
		if t.id == activeID {
			tab = t
			break
		}
	}
	e.tabsMu.RUnlock()

	if tab == nil {
		return
	}

	if tab.IsModified() {
		fileName := filepath.Base(tab.path)
		if e.dialogManager != nil {
			e.dialogManager.ShowUnsavedChangesDialog(fileName,
				func() {
					e.doCloseCurrentTab()
				},
				func() {
				})
		}
	} else {
		e.doCloseCurrentTab()
	}
}

func (e *Editor) doCloseCurrentTab() {
	activeID := e.tabManager.GetActiveTabID()
	if activeID < 0 {
		return
	}

	e.tabManager.RemoveTab(activeID)
}

func (e *Editor) releaseTabResources(tab *EditorTab) {
	if tab == nil {
		return
	}
	if tab.codeEditor != nil {
		tab.codeEditor.Close()
		tab.codeEditor = nil
	}
	if tab.container != nil {
		tab.container.Clear()
		tab.container = nil
	}
	tab.searchBar = nil
}

func (e *Editor) rebuildPages() {
}

// SaveCurrentFile saves the currently active file to disk.
func (e *Editor) SaveCurrentFile() {
	activeID := e.tabManager.GetActiveTabID()
	if activeID < 0 {
		return
	}

	e.tabsMu.RLock()
	var tab *EditorTab
	for _, t := range e.tabs {
		if t.id == activeID {
			tab = t
			break
		}
	}
	e.tabsMu.RUnlock()

	if tab == nil || tab.codeEditor == nil {
		return
	}

	if tab.path == "" {
		// TODO: Show save dialog
		return
	}

	if tab.externallyModified {
		if _, err := os.Stat(tab.path); os.IsNotExist(err) {
			e.showDeletedFileDialog(tab)
		} else {
			e.showSaveConflictDialog(tab)
		}
		return
	}

	e.saveTab(tab, tab.path)
}

// saveTab writes the tab's buffer to path. On success the tab takes path,
// is marked clean and onFileSaved fires. On failure the tab keeps its
// path and dirty state, and the error is logged and shown to the user.
func (e *Editor) saveTab(tab *EditorTab, path string) bool {
	if err := tab.codeEditor.SaveFile(path); err != nil {
		e.reportSaveError(path, err)
		return false
	}

	if tab.path != path {
		e.tabsMu.Lock()
		if e.tabsByPath[tab.path] == tab {
			delete(e.tabsByPath, tab.path)
		}
		e.tabsByPath[path] = tab
		tab.path = path
		e.tabsMu.Unlock()
	}
	tab.externallyModified = false
	if info, err := os.Stat(path); err == nil {
		tab.lastModTime = info.ModTime()
	}
	tab.modified = false
	e.UpdateTabBar()

	if e.onFileSaved != nil {
		e.onFileSaved(path)
	}
	return true
}

// reportSaveError logs a failed save and tells the user about it.
func (e *Editor) reportSaveError(path string, err error) {
	logging.Error("Failed to save %s: %v", path, err)
	if e.dialogManager != nil {
		e.dialogManager.ShowSaveErrorDialog(path, err)
	}
}

// GetView returns the root primitive for the editor component.
func (e *Editor) GetView() tview.Primitive {
	return e.container
}

// HasFocus checks if the given primitive is part of the editor component.
func (e *Editor) HasFocus(focused tview.Primitive) bool {
	if focused == e.container || focused == e.contentArea {
		return true
	}
	e.tabsMu.RLock()
	for _, tab := range e.tabs {
		if focused == tab.codeEditor || focused == tab.container ||
			(tab.searchBar != nil && tab.searchBar.HasFocus()) {
			e.tabsMu.RUnlock()
			return true
		}
	}
	e.tabsMu.RUnlock()
	if focused == e.noDocsView {
		return true
	}
	return false
}

// UpdateFilePath updates the file path for a tab when a file is renamed.
func (e *Editor) UpdateFilePath(oldPath, newPath string) {
	e.tabsMu.Lock()
	for _, tab := range e.tabs {
		if tab.path == oldPath {
			tab.path = newPath
			e.tabsMu.Unlock()
			e.UpdateTabBar()
			return
		}
	}
	e.tabsMu.Unlock()
}

// SetApp sets the application reference and initializes the dialog manager.
func (e *Editor) SetApp(app *tview.Application) {
	e.app = app
	e.dialogManager = NewEditorDialogManager(app)
}

func (e *Editor) SetSingleFileMode(enabled bool) {
	e.singleFileMode = enabled
	e.UpdateTabBar()
}

// SetOnLastTabClosed sets a callback for when the last editor tab is closed.
func (e *Editor) SetOnLastTabClosed(handler func()) {
	e.onLastTabClosed = handler
}

// SetOnTabClosed sets a callback for when any editor tab is closed.
func (e *Editor) SetOnTabClosed(handler func(path string)) {
	e.onTabClosed = handler
}

// SetOnActiveFileChanged sets a callback for when the active file changes.
func (e *Editor) SetOnActiveFileChanged(handler func(path string)) {
	e.onActiveFileChanged = handler
}

// SetOnSelectionChange sets a callback for when the selection changes in any editor.
func (e *Editor) SetOnSelectionChange(handler func(path string, text string, startRow, startCol, endRow, endCol int)) {
	e.onSelectionChange = handler
}

// SetOnFileSaved sets a callback for when a file is saved.
func (e *Editor) SetOnFileSaved(handler func(path string)) {
	e.onFileSaved = handler
}

// HasTabs returns true if there are any open editor tabs.
func (e *Editor) HasTabs() bool {
	return e.tabManager.HasTabs()
}

// HasUnsavedChanges returns true if any open tab has unsaved changes.
func (e *Editor) HasUnsavedChanges() bool {
	for _, tab := range e.tabs {
		if tab.modified {
			return true
		}
	}
	return false
}

// CloseFileTab closes the tab for the specified file path.
func (e *Editor) CloseFileTab(path string) {
	e.tabsMu.RLock()
	tab, exists := e.tabsByPath[path]
	e.tabsMu.RUnlock()

	if exists {
		e.tabManager.RemoveTab(tab.id)
	}
}

// GetNoDocsView returns the view shown when no documents are open.
func (e *Editor) GetNoDocsView() tview.Primitive {
	return e.noDocsView
}

func (e *Editor) showSaveConflictDialog(tab *EditorTab) {
	if e.dialogManager == nil {
		return
	}

	e.dialogManager.ShowSaveConflictDialog(
		tab.path,
		func() {
			e.saveTab(tab, tab.path)
		},
		func(copyPath string) {
			e.saveTab(tab, copyPath)
		},
		func() {
		},
	)
}

// HandleFileModified handles external modifications to open files.
func (e *Editor) HandleFileModified(filePath string) {
	e.tabsMu.Lock()
	for _, tab := range e.tabs {
		if tab.path == filePath {
			fileInfo := &components.FileInfo{
				Path:               tab.path,
				Modified:           tab.modified,
				ExternallyModified: tab.externallyModified,
				LastModTime:        tab.lastModTime,
			}

			action := e.fileChangeHandler.HandleFileModified(fileInfo)

			switch action {
			case components.FileChangeActionReload:
				e.tabsMu.Unlock()
				e.reloadTab(tab)
				return
			case components.FileChangeActionMarkConflict:
				tab.externallyModified = true
			}
			e.tabsMu.Unlock()
			return
		}
	}
	e.tabsMu.Unlock()
}

// HandleFileDeleted handles external deletion of open files.
func (e *Editor) HandleFileDeleted(filePath string) {
	e.tabsMu.Lock()
	for _, tab := range e.tabs {
		if tab.path == filePath {
			fileInfo := &components.FileInfo{
				Path:               tab.path,
				Modified:           tab.modified,
				ExternallyModified: tab.externallyModified,
				LastModTime:        tab.lastModTime,
			}

			action := e.fileChangeHandler.HandleFileDeleted(fileInfo)

			switch action {
			case components.FileChangeActionClose:
				tabID := tab.id
				e.tabsMu.Unlock()
				e.tabManager.RemoveTab(tabID)
				return
			case components.FileChangeActionMarkDeleted:
				tab.externallyModified = true
			}
			e.tabsMu.Unlock()
			return
		}
	}
	e.tabsMu.Unlock()
}

func (e *Editor) reloadTab(tab *EditorTab) {
	content, err := os.ReadFile(tab.path)
	if err != nil {
		logging.Error("Failed to reload file %s: %v", tab.path, err)
		return
	}

	tab.codeEditor.SetContent(string(content))
	tab.modified = false
	tab.externallyModified = false

	if info, err := os.Stat(tab.path); err == nil {
		tab.lastModTime = info.ModTime()
	}

	if tab.id == e.tabManager.GetActiveTabID() {
		e.UpdateTabBar()
	}
}

func (e *Editor) showDeletedFileDialog(tab *EditorTab) {
	if e.dialogManager == nil {
		return
	}

	e.dialogManager.ShowDeletedFileDialog(
		tab.path,
		func() {
			e.saveTab(tab, tab.path)
		},
		func() {
			e.tabManager.RemoveTab(tab.id)
		},
		func() {
		},
	)
}

// setupSearchBar configures the search bar for a tab.
func (e *Editor) setupSearchBar(tab *EditorTab) {
	tab.searchBar.SetCallbacks(
		func(text string, caseSensitive, wholeWord, useRegex bool) {
			e.performSearch(tab, text, caseSensitive, wholeWord, useRegex)
		},
		func(searchText, replaceText string) {
			e.replaceCurrentMatch(tab, replaceText)
		},
		func(searchText, replaceText string) {
			e.replaceAllMatches(tab, replaceText)
		},
		func() {
			e.findNext(tab)
		},
		func() {
			e.findPrevious(tab)
		},
		func() {
			e.hideSearch(tab)
		},
	)
}

// IsSearchBarActive reports if the active tab has the search bar open.
func (e *Editor) IsSearchBarActive() bool {
	tab := e.getCurrentTab()
	return tab != nil && tab.searchVisible
}

// ShowSearch shows the search bar for the current tab.
func (e *Editor) ShowSearch() {
	e.tabsMu.RLock()
	currentTab := e.getCurrentTab()
	e.tabsMu.RUnlock()

	if currentTab == nil {
		return
	}

	if !currentTab.searchVisible {
		currentTab.searchVisible = true
		currentTab.container.RemoveItem(currentTab.codeEditor)
		currentTab.container.AddItem(currentTab.codeEditor, 0, 1, false)
		currentTab.container.AddItem(currentTab.searchBar, searchBarHeight, 0, true)

		selectedText := currentTab.codeEditor.GetSelectedText()
		if selectedText != "" {
			currentTab.searchBar.SetSearchText(selectedText)
		}

		if e.app != nil {
			e.app.SetFocus(currentTab.searchBar)
		}
	}
}

// hideSearch hides the search bar for the specified tab.
func (e *Editor) hideSearch(tab *EditorTab) {
	if tab.searchVisible {
		tab.searchVisible = false
		tab.container.RemoveItem(tab.searchBar)
		tab.container.RemoveItem(tab.codeEditor)
		tab.container.AddItem(tab.codeEditor, 0, 1, true)

		tab.codeEditor.ClearSearchHighlights()
		tab.codeEditor.buffer.ClearSearchMatches()

		if e.app != nil {
			e.app.SetFocus(tab.codeEditor)
		}
	}
}

// performSearch performs a search in the specified tab.
func (e *Editor) performSearch(tab *EditorTab, text string, caseSensitive, wholeWord, useRegex bool) {
	if text == "" {
		tab.codeEditor.ClearSearchHighlights()
		tab.codeEditor.buffer.ClearSearchMatches()
		tab.searchBar.UpdateMatches(0, 0)
		return
	}

	matches := tab.codeEditor.Search(text, caseSensitive, wholeWord, useRegex)
	tab.searchBar.UpdateMatches(tab.codeEditor.buffer.GetCurrentMatch())

	if len(matches) > 0 {
		row, col, _ := tab.codeEditor.buffer.NextMatch()
		tab.codeEditor.SetCursor(row, col)
		tab.codeEditor.ScrollToCursor()
	}
}

// findNext moves to the next search match.
func (e *Editor) findNext(tab *EditorTab) {
	row, col, found := tab.codeEditor.buffer.NextMatch()
	if found {
		tab.codeEditor.SetCursor(row, col)
		tab.codeEditor.ScrollToCursor()
		tab.searchBar.UpdateMatches(tab.codeEditor.buffer.GetCurrentMatch())
	}
}

// findPrevious moves to the previous search match.
func (e *Editor) findPrevious(tab *EditorTab) {
	row, col, found := tab.codeEditor.buffer.PreviousMatch()
	if found {
		tab.codeEditor.SetCursor(row, col)
		tab.codeEditor.ScrollToCursor()
		tab.searchBar.UpdateMatches(tab.codeEditor.buffer.GetCurrentMatch())
	}
}

// replaceCurrentMatch replaces the current search match.
func (e *Editor) replaceCurrentMatch(tab *EditorTab, replaceText string) {
	if tab.codeEditor.buffer.ReplaceCurrentMatch(replaceText) {
		tab.searchBar.UpdateMatches(tab.codeEditor.buffer.GetCurrentMatch())
		tab.codeEditor.setModified(true)
		tab.codeEditor.updateHighlighter()
		e.UpdateTabBar()
	}
}

// replaceAllMatches replaces all search matches.
func (e *Editor) replaceAllMatches(tab *EditorTab, replaceText string) {
	count := tab.codeEditor.buffer.ReplaceAllMatches(replaceText)
	if count > 0 {
		tab.codeEditor.ClearSearchHighlights()
		tab.searchBar.UpdateMatches(0, 0)
		tab.codeEditor.setModified(true)
		tab.codeEditor.updateHighlighter()
		e.UpdateTabBar()
	}
}

// getCurrentTab returns the currently active tab.
func (e *Editor) getCurrentTab() *EditorTab {
	activeID := e.tabManager.GetActiveTabID()
	for _, tab := range e.tabs {
		if tab.id == activeID {
			return tab
		}
	}
	return nil
}

// GetOpenTabs returns a map of all open tabs keyed by their file path
func (e *Editor) GetOpenTabs() map[string]*EditorTab {
	openTabs := make(map[string]*EditorTab)
	for _, tab := range e.tabs {
		if tab.path != "" {
			openTabs[tab.path] = tab
		}
	}
	return openTabs
}

// GetCurrentFileAndLine returns the current file path and 1-based line number.
func (e *Editor) GetCurrentFileAndLine() (string, int) {
	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return "", 0
	}
	row, _ := tab.codeEditor.GetCursor()
	return tab.path, row + 1
}

// GetOpenFilePaths returns a list of all open file paths.
func (e *Editor) GetOpenFilePaths() []string {
	paths := make([]string, 0, len(e.tabs))
	for _, tab := range e.tabs {
		if tab.path != "" {
			paths = append(paths, tab.path)
		}
	}
	return paths
}

// GetActiveFilePath returns the path of the currently active file.
func (e *Editor) GetActiveFilePath() string {
	path, _ := e.GetCurrentFileAndLine()
	return path
}

// GetPersistedFilePaths returns open file paths worth persisting in the
// session, excluding preview tabs, in tab order.
func (e *Editor) GetPersistedFilePaths() []string {
	paths := make([]string, 0, len(e.tabs))
	for _, tab := range e.tabs {
		if tab.path != "" && !tab.isPreview {
			paths = append(paths, tab.path)
		}
	}
	return paths
}

// GetActiveCursorLine returns the 1-based line number of the cursor in the active file.
func (e *Editor) GetActiveCursorLine() int {
	_, line := e.GetCurrentFileAndLine()
	return line
}

// GoToLine moves the cursor to the specified 1-based line number in the active file.
func (e *Editor) GoToLine(line int) {
	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return
	}
	tab.codeEditor.SetCursor(line-1, 0)
	tab.codeEditor.CenterViewOnCursor()
}

// GoToPosition moves the cursor to the specified 1-based line and column.
func (e *Editor) GoToPosition(line, col int) {
	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return
	}
	tab.codeEditor.SetCursor(line-1, col-1)
	tab.codeEditor.CenterViewOnCursor()
}

// SaveFileByPath saves a specific file by path.
func (e *Editor) SaveFileByPath(path string) bool {
	for _, tab := range e.tabs {
		if tab.path == path && tab.codeEditor != nil {
			if err := tab.codeEditor.SaveFile(tab.path); err == nil {
				tab.modified = false
				e.UpdateTabBar()
				if e.onFileSaved != nil {
					e.onFileSaved(tab.path)
				}
				return true
			}
		}
	}
	return false
}

// GetBufferContent returns the content of a file's buffer (includes unsaved changes).
func (e *Editor) GetBufferContent(path string) string {
	for _, tab := range e.tabs {
		if tab.path == path && tab.codeEditor != nil {
			lines := tab.codeEditor.buffer.GetLines()
			strLines := make([]string, len(lines))
			for i, line := range lines {
				strLines[i] = string(line)
			}
			return strings.Join(strLines, "\n")
		}
	}
	return ""
}

// GetCurrentBufferContent returns the content of the active file's buffer.
func (e *Editor) GetCurrentBufferContent() string {
	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return ""
	}
	lines := tab.codeEditor.buffer.GetLines()
	strLines := make([]string, len(lines))
	for i, line := range lines {
		strLines[i] = string(line)
	}
	return strings.Join(strLines, "\n")
}

// GetSelection returns the currently selected text in the active editor.
func (e *Editor) GetSelection() string {
	tab := e.getCurrentTab()
	if tab == nil || tab.codeEditor == nil {
		return ""
	}
	return tab.codeEditor.GetSelectedText()
}

// GetModifiedFiles returns a list of file paths with unsaved changes.
func (e *Editor) GetModifiedFiles() []string {
	var modified []string
	for _, tab := range e.tabs {
		if tab.modified && tab.path != "" {
			modified = append(modified, tab.path)
		}
	}
	return modified
}

// IsFileModified returns true if the specified file has unsaved changes.
func (e *Editor) IsFileModified(path string) bool {
	for _, tab := range e.tabs {
		if tab.path == path {
			return tab.modified
		}
	}
	return false
}

// CloseTabByPath closes a file tab without prompting for save.
func (e *Editor) CloseTabByPath(path string) bool {
	e.tabsMu.RLock()
	var tabID int = -1
	for _, tab := range e.tabs {
		if tab.path == path {
			tabID = tab.id
			break
		}
	}
	e.tabsMu.RUnlock()

	if tabID != -1 {
		e.tabManager.RemoveTab(tabID)
		return true
	}
	return false
}

// SaveAllModifiedFiles saves all files with unsaved changes.
func (e *Editor) SaveAllModifiedFiles() int {
	saved := 0
	for _, tab := range e.tabs {
		if tab.modified && tab.path != "" && tab.codeEditor != nil {
			if err := tab.codeEditor.SaveFile(tab.path); err == nil {
				tab.modified = false
				saved++
				if e.onFileSaved != nil {
					e.onFileSaved(tab.path)
				}
			}
		}
	}
	if saved > 0 {
		e.UpdateTabBar()
	}
	return saved
}

// GetTabCount returns the number of open editor tabs.
func (e *Editor) GetTabCount() int {
	return len(e.tabs)
}

// GetAllTabs returns all open editor tabs.
func (e *Editor) GetAllTabs() []*EditorTab {
	e.tabsMu.RLock()
	defer e.tabsMu.RUnlock()

	result := make([]*EditorTab, len(e.tabs))
	copy(result, e.tabs)
	return result
}

// GetActiveTab returns the currently active editor tab.
func (e *Editor) GetActiveTab() *EditorTab {
	activeID := e.tabManager.GetActiveTabID()
	e.tabsMu.RLock()
	defer e.tabsMu.RUnlock()
	return e.tabsByID[activeID]
}

// GetTabByPath returns the editor tab for the given file path.
func (e *Editor) GetTabByPath(path string) *EditorTab {
	e.tabsMu.RLock()
	defer e.tabsMu.RUnlock()
	return e.tabsByPath[path]
}

// ReloadFile reloads a file from disk if it's open in the editor.
func (e *Editor) ReloadFile(path string) {
	e.tabsMu.RLock()
	tab := e.tabsByPath[path]
	e.tabsMu.RUnlock()

	if tab != nil {
		e.reloadTab(tab)
	}
}

// GetCursor returns the current cursor position (row, col) for the code editor.
func (c *CodeEditor) GetCursor() (int, int) {
	return c.cursorRow, c.cursorCol
}
