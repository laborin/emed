package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/laborin/emed/internal/config"
	"github.com/laborin/emed/internal/logging"
)

type EditorProvider interface {
	OpenFile(path string, startLine, endLine int, selectPattern string) error
	CloseTab(path string) error
	SaveDocument(path string, content string) error
	GetOpenEditors() []OpenEditor
	CheckDocumentDirty(path string) bool
	GetCurrentSelection() *Selection
	GetLatestSelection() *Selection
}

type DiffProvider interface {
	OpenDiff(filePath, oldContent, newContent, tabName string, sessionID int) (result string, finalContent string, err error)
	CloseAllDiffTabs()
	CloseDiffTabByName(tabName string) bool
}

type TerminalProvider interface {
	ExecuteCode(code string, language string) error
}

type WorkspaceProvider interface {
	GetWorkspaceFolders() []WorkspaceFolder
}

type ToolsHandler struct {
	Editor    EditorProvider
	Diff      DiffProvider
	Terminal  TerminalProvider
	Workspace WorkspaceProvider
}

func NewToolsHandler() *ToolsHandler {
	return &ToolsHandler{}
}

func (h *ToolsHandler) HandleTool(sessionID int, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "initialize":
		return h.handleInitialize(params)
	case "initialized", "notifications/initialized":
		return map[string]interface{}{}, nil
	case "ide_connected":
		return map[string]interface{}{}, nil
	case "tools/list":
		return h.handleToolsList()
	case "tools/call":
		return h.handleToolsCall(sessionID, params)
	case "prompts/list":
		return h.handlePromptsList()
	case "prompts/get":
		return h.handlePromptsGet(params)
	case "resources/list":
		return h.handleResourcesList()
	case "resources/templates/list":
		return h.handleResourcesTemplatesList()
	case "resources/read":
		return h.handleResourcesRead(params)
	case ToolOpenFile:
		return h.handleOpenFile(params)
	case ToolOpenDiff:
		return h.handleOpenDiff(sessionID, params)
	case ToolCloseTab:
		return h.handleCloseTab(params)
	case ToolCloseAllDiffTabs:
		return h.handleCloseAllDiffTabs()
	case ToolGetCurrentSelect:
		return h.handleGetCurrentSelection()
	case ToolGetLatestSelect:
		return h.handleGetLatestSelection()
	case ToolGetOpenEditors:
		return h.handleGetOpenEditors()
	case ToolGetWorkspace:
		return h.handleGetWorkspaceFolders()
	case ToolGetDiagnostics:
		return h.handleGetDiagnostics(params)
	case ToolCheckDocDirty:
		return h.handleCheckDocumentDirty(params)
	case ToolSaveDocument:
		return h.handleSaveDocument(params)
	case ToolExecuteCode:
		return h.handleExecuteCode(params)
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

func (h *ToolsHandler) handleResourcesTemplatesList() (interface{}, error) {
	return map[string]interface{}{
		"resourceTemplates": []map[string]interface{}{},
	}, nil
}

func (h *ToolsHandler) handleInitialize(params json.RawMessage) (interface{}, error) {
	// Build instructions with current editor state
	instructions := "emed is a terminal-based text editor. "
	if h.Editor != nil {
		editors := h.Editor.GetOpenEditors()
		if len(editors) > 0 {
			instructions += "Currently open files: "
			for i, ed := range editors {
				if i > 0 {
					instructions += ", "
				}
				instructions += ed.FilePath
				if ed.IsActive {
					instructions += " (active)"
				}
			}
			instructions += ". "
		} else {
			instructions += "No files currently open. "
		}
	}
	instructions += "Use @ mentions to reference resources like @emed:editor://openEditors for the full list of open editors."

	return map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"logging":   map[string]interface{}{},
			"prompts":   map[string]interface{}{"listChanged": true},
			"resources": map[string]interface{}{"subscribe": true, "listChanged": true},
			"tools":     map[string]interface{}{"listChanged": true},
		},
		"serverInfo": map[string]interface{}{
			"name":         "emed",
			"version":      config.Version(),
			"instructions": instructions,
		},
	}, nil
}

func (h *ToolsHandler) handlePromptsList() (interface{}, error) {
	prompts := []map[string]interface{}{
		{
			"name":        "editor-context",
			"description": "Get current editor state including open files and selection",
		},
	}
	return map[string]interface{}{"prompts": prompts}, nil
}

type PromptGetParams struct {
	Name string `json:"name"`
}

func (h *ToolsHandler) handlePromptsGet(params json.RawMessage) (interface{}, error) {
	var p PromptGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid prompts/get params: %w", err)
	}

	if p.Name != "editor-context" {
		return nil, fmt.Errorf("unknown prompt: %s", p.Name)
	}

	// Build context message with current editor state
	var context string
	if h.Editor != nil {
		editors := h.Editor.GetOpenEditors()
		if len(editors) > 0 {
			context = "## Open Files in emed\n\n"
			for _, ed := range editors {
				status := ""
				if ed.IsActive {
					status = " (active)"
				}
				if ed.IsDirty {
					status += " (modified)"
				}
				context += fmt.Sprintf("- %s%s\n", ed.FilePath, status)
			}
		} else {
			context = "No files currently open in emed.\n"
		}

		if sel := h.Editor.GetCurrentSelection(); sel != nil && sel.Text != "" {
			context += fmt.Sprintf("\n## Current Selection\nFile: %s\nLines %d-%d:\n```\n%s\n```\n",
				sel.FilePath, sel.Range.Start.Line+1, sel.Range.End.Line+1, sel.Text)
		}
	} else {
		context = "Editor not available.\n"
	}

	return map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": map[string]interface{}{
					"type": "text",
					"text": context,
				},
			},
		},
	}, nil
}

func (h *ToolsHandler) handleToolsList() (interface{}, error) {
	baseSchema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"$schema":              "http://json-schema.org/draft-07/schema#",
	}

	schemaWithProps := func(props map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"$schema":              "http://json-schema.org/draft-07/schema#",
			"properties":           props,
		}
	}

	tools := []map[string]interface{}{
		{"name": ToolOpenFile, "description": "Open a file in the editor at an optional line", "inputSchema": schemaWithProps(map[string]interface{}{
			"filePath":  map[string]interface{}{"type": "string", "description": "Path to the file to open"},
			"startLine": map[string]interface{}{"type": "integer", "description": "Line number to scroll to"},
			"endLine":   map[string]interface{}{"type": "integer", "description": "End line for selection"},
		})},
		{"name": ToolOpenDiff, "description": "Show a diff and wait for user to accept or reject", "inputSchema": schemaWithProps(map[string]interface{}{
			"old_file_path":     map[string]interface{}{"type": "string", "description": "Path to the original file"},
			"new_file_path":     map[string]interface{}{"type": "string", "description": "Path for the new file"},
			"new_file_contents": map[string]interface{}{"type": "string", "description": "New file contents"},
			"tab_name":          map[string]interface{}{"type": "string", "description": "Display name for the diff tab"},
		})},
		{"name": ToolCloseTab, "description": "Close a file tab", "inputSchema": schemaWithProps(map[string]interface{}{
			"filePath": map[string]interface{}{"type": "string", "description": "Path to the file to close"},
		})},
		{"name": ToolCloseAllDiffTabs, "description": "Close all diff tabs", "inputSchema": baseSchema},
		{"name": ToolGetCurrentSelect, "description": "Get current selection in active editor", "inputSchema": baseSchema},
		{"name": ToolGetLatestSelect, "description": "Get most recent selection", "inputSchema": baseSchema},
		{"name": ToolGetOpenEditors, "description": "Get list of currently open files in the editor", "inputSchema": baseSchema},
		{"name": ToolGetWorkspace, "description": "Get workspace folders", "inputSchema": baseSchema},
		{"name": ToolGetDiagnostics, "description": "Get diagnostics (errors, warnings) for a file", "inputSchema": schemaWithProps(map[string]interface{}{
			"filePath": map[string]interface{}{"type": "string", "description": "Path to the file"},
		})},
		{"name": ToolCheckDocDirty, "description": "Check if document has unsaved changes", "inputSchema": schemaWithProps(map[string]interface{}{
			"filePath": map[string]interface{}{"type": "string", "description": "Path to the file"},
		})},
		{"name": ToolSaveDocument, "description": "Save content to a file", "inputSchema": schemaWithProps(map[string]interface{}{
			"filePath": map[string]interface{}{"type": "string", "description": "Path to the file"},
			"content":  map[string]interface{}{"type": "string", "description": "Content to save"},
		})},
		{"name": ToolExecuteCode, "description": "Execute code in terminal", "inputSchema": schemaWithProps(map[string]interface{}{
			"code":     map[string]interface{}{"type": "string", "description": "Code to execute"},
			"language": map[string]interface{}{"type": "string", "description": "Programming language"},
		})},
	}
	return map[string]interface{}{"tools": tools}, nil
}

func (h *ToolsHandler) handleResourcesList() (interface{}, error) {
	logging.Debug("Claude: resources/list called")

	// Get current open editors to include in resource list
	var openEditorsDesc string
	if h.Editor != nil {
		editors := h.Editor.GetOpenEditors()
		if len(editors) > 0 {
			paths := make([]string, len(editors))
			for i, ed := range editors {
				paths[i] = ed.FilePath
			}
			openEditorsDesc = fmt.Sprintf("Currently open: %v", paths)
		} else {
			openEditorsDesc = "No files currently open"
		}
	} else {
		openEditorsDesc = "Editor not available"
	}

	resources := []map[string]interface{}{
		{
			"uri":         "editor://openEditors",
			"name":        "Open Editors",
			"description": openEditorsDesc,
			"mimeType":    "application/json",
		},
		{
			"uri":         "editor://currentSelection",
			"name":        "Current Selection",
			"description": "Currently selected text in the active editor",
			"mimeType":    "application/json",
		},
		{
			"uri":         "editor://activeFile",
			"name":        "Active File",
			"description": "The currently active file in the editor",
			"mimeType":    "application/json",
		},
	}

	result := map[string]interface{}{"resources": resources}
	resultJSON, _ := json.Marshal(result)
	logging.Debug("Claude: resources/list response: %s", string(resultJSON))
	return result, nil
}

type ResourceReadParams struct {
	URI string `json:"uri"`
}

func (h *ToolsHandler) handleResourcesRead(params json.RawMessage) (interface{}, error) {
	var p ResourceReadParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid resources/read params: %w", err)
	}

	logging.Debug("Claude: resources/read called for URI: %s", p.URI)

	var content string
	var mimeType string

	switch p.URI {
	case "editor://openEditors":
		if h.Editor == nil {
			content = "[]"
		} else {
			editors := h.Editor.GetOpenEditors()
			data, _ := json.Marshal(editors)
			content = string(data)
		}
		mimeType = "application/json"

	case "editor://currentSelection":
		if h.Editor == nil {
			content = "null"
		} else {
			sel := h.Editor.GetCurrentSelection()
			if sel == nil {
				content = "null"
			} else {
				data, _ := json.Marshal(sel)
				content = string(data)
			}
		}
		mimeType = "application/json"

	case "editor://activeFile":
		if h.Editor == nil {
			content = "null"
		} else {
			editors := h.Editor.GetOpenEditors()
			var activeEditor *OpenEditor
			for _, ed := range editors {
				if ed.IsActive {
					activeEditor = &ed
					break
				}
			}
			if activeEditor == nil {
				content = "null"
			} else {
				data, _ := json.Marshal(activeEditor)
				content = string(data)
			}
		}
		mimeType = "application/json"

	default:
		return nil, fmt.Errorf("unknown resource: %s", p.URI)
	}

	// Return in MCP resource format
	return map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"uri":      p.URI,
				"mimeType": mimeType,
				"text":     content,
			},
		},
	}, nil
}

type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *ToolsHandler) handleToolsCall(sessionID int, params json.RawMessage) (interface{}, error) {
	var p ToolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}

	// Dispatch to the actual tool handler
	switch p.Name {
	case ToolOpenFile:
		return h.handleOpenFile(p.Arguments)
	case ToolOpenDiff:
		return h.handleOpenDiff(sessionID, p.Arguments)
	case ToolCloseTab:
		return h.handleCloseTab(p.Arguments)
	case ToolCloseAllDiffTabs:
		return h.handleCloseAllDiffTabs()
	case ToolGetCurrentSelect:
		return h.handleGetCurrentSelection()
	case ToolGetLatestSelect:
		return h.handleGetLatestSelection()
	case ToolGetOpenEditors:
		return h.handleGetOpenEditors()
	case ToolGetWorkspace:
		return h.handleGetWorkspaceFolders()
	case ToolGetDiagnostics:
		return h.handleGetDiagnostics(p.Arguments)
	case ToolCheckDocDirty:
		return h.handleCheckDocumentDirty(p.Arguments)
	case ToolSaveDocument:
		return h.handleSaveDocument(p.Arguments)
	case ToolExecuteCode:
		return h.handleExecuteCode(p.Arguments)
	default:
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}
}

func (h *ToolsHandler) handleOpenFile(params json.RawMessage) (interface{}, error) {
	var p OpenFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	if err := h.Editor.OpenFile(p.FilePath, p.StartLine, p.EndLine, p.SelectPattern); err != nil {
		return nil, err
	}

	return NewMCPTextResponse("File opened"), nil
}

func (h *ToolsHandler) handleOpenDiff(sessionID int, params json.RawMessage) (interface{}, error) {
	var p OpenDiffParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	logging.Debug("openDiff parsed: oldPath=%s, newPath=%s, newLen=%d, tabName=%s",
		p.OldFilePath, p.NewFilePath, len(p.NewFileContent), p.TabName)

	if h.Diff == nil {
		return nil, fmt.Errorf("diff view not available")
	}

	oldContent, err := readFileContent(p.OldFilePath)
	if err != nil {
		oldContent = ""
	}

	targetPath := p.NewFilePath
	if targetPath == "" {
		targetPath = p.OldFilePath
	}

	result, finalContent, err := h.Diff.OpenDiff(targetPath, oldContent, p.NewFileContent, p.TabName, sessionID)
	if err != nil {
		return nil, err
	}

	if result == DiffResultSaved {
		return NewMCPDiffResponse(result, finalContent), nil
	}
	return NewMCPTextResponse(result), nil
}

func (h *ToolsHandler) handleCloseTab(params json.RawMessage) (interface{}, error) {
	var p CloseTabParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if p.TabName != "" {
		if h.Diff == nil {
			return nil, fmt.Errorf("diff view not available")
		}
		if h.Diff.CloseDiffTabByName(p.TabName) {
			return NewMCPTextResponse("Tab closed"), nil
		}
		return NewMCPTextResponse("Tab not found"), nil
	}

	if p.FilePath == "" {
		return nil, fmt.Errorf("missing filePath or tab_name")
	}

	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	if err := h.Editor.CloseTab(p.FilePath); err != nil {
		return nil, err
	}

	return NewMCPTextResponse("Tab closed"), nil
}

func (h *ToolsHandler) handleCloseAllDiffTabs() (interface{}, error) {
	if h.Diff == nil {
		return nil, fmt.Errorf("diff view not available")
	}

	h.Diff.CloseAllDiffTabs()
	return NewMCPTextResponse("All diff tabs closed"), nil
}

func (h *ToolsHandler) handleGetCurrentSelection() (interface{}, error) {
	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	sel := h.Editor.GetCurrentSelection()
	if sel == nil {
		return NewMCPTextResponse("null"), nil
	}

	data, _ := json.Marshal(sel)
	return NewMCPTextResponse(string(data)), nil
}

func (h *ToolsHandler) handleGetLatestSelection() (interface{}, error) {
	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	sel := h.Editor.GetLatestSelection()
	if sel == nil {
		return NewMCPTextResponse("null"), nil
	}

	data, _ := json.Marshal(sel)
	return NewMCPTextResponse(string(data)), nil
}

func (h *ToolsHandler) handleGetOpenEditors() (interface{}, error) {
	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	editors := h.Editor.GetOpenEditors()
	data, _ := json.Marshal(editors)
	return NewMCPTextResponse(string(data)), nil
}

func (h *ToolsHandler) handleGetWorkspaceFolders() (interface{}, error) {
	if h.Workspace == nil {
		return nil, fmt.Errorf("workspace not available")
	}

	folders := h.Workspace.GetWorkspaceFolders()
	data, _ := json.Marshal(folders)
	return NewMCPTextResponse(string(data)), nil
}

func (h *ToolsHandler) handleGetDiagnostics(params json.RawMessage) (interface{}, error) {
	return NewMCPTextResponse("[]"), nil
}

func (h *ToolsHandler) handleCheckDocumentDirty(params json.RawMessage) (interface{}, error) {
	var p CheckDocumentDirtyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	isDirty := h.Editor.CheckDocumentDirty(p.FilePath)
	return NewMCPTextResponse(fmt.Sprintf("%t", isDirty)), nil
}

func (h *ToolsHandler) handleSaveDocument(params json.RawMessage) (interface{}, error) {
	var p SaveDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if h.Editor == nil {
		return nil, fmt.Errorf("editor not available")
	}

	if err := h.Editor.SaveDocument(p.FilePath, p.Content); err != nil {
		return nil, err
	}

	return NewMCPTextResponse("Document saved"), nil
}

func (h *ToolsHandler) handleExecuteCode(params json.RawMessage) (interface{}, error) {
	var p ExecuteCodeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if h.Terminal == nil {
		return nil, fmt.Errorf("terminal not available")
	}

	if err := h.Terminal.ExecuteCode(p.Code, p.Language); err != nil {
		return nil, err
	}

	return NewMCPTextResponse("Code executed"), nil
}

func readFileContent(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
