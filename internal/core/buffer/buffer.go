// Package buffer provides text buffer functionality with undo/redo support.
package buffer

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"time"
)

// OpType represents the type of edit operation
type OpType int

const (
	OpInsert OpType = iota
	OpDelete
	OpNewline
	OpDeleteNewline
)

// EditOp represents a single edit operation for undo/redo
type EditOp struct {
	ID        uint64
	Type      OpType
	Row       int
	Col       int
	Text      string
	CursorRow int
	CursorCol int
	Timestamp time.Time
}

// TextBuffer represents a text buffer with undo/redo functionality.
type TextBuffer struct {
	lines           [][]rune
	lineEndings     string
	trailingNewline bool
	modified        bool
	tabSize         int

	// Operation-based undo/redo stacks
	undoStack    []EditOp
	redoStack    []EditOp
	maxUndoSize  int
	savePointIdx int
	savePointID  uint64
	nextOpID     uint64

	// Coalescing: group consecutive char inserts/deletes
	lastOpTime    time.Time
	coalesceDelay time.Duration

	// Track if we should record this change
	recordingUndo bool

	// Search state
	searchMatches []SearchMatch
	currentMatch  int
	searchPattern string
	searchRegex   *regexp.Regexp
}

// SearchMatch represents a search match in the buffer.
type SearchMatch struct {
	StartRow int
	StartCol int
	EndRow   int
	EndCol   int
}

// New creates a new empty text buffer.
func New() *TextBuffer {
	return &TextBuffer{
		lines:         [][]rune{{}},
		lineEndings:   "\n",
		tabSize:       4,
		modified:      false,
		undoStack:     make([]EditOp, 0, 256),
		redoStack:     make([]EditOp, 0, 64),
		maxUndoSize:   1000,
		coalesceDelay: 500 * time.Millisecond,
		recordingUndo: true,
		savePointIdx:  0,
		savePointID:   0,
		nextOpID:      1,
	}
}

// LoadFile loads content from a file into the buffer.
func (b *TextBuffer) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	b.setContentInternal(string(data))
	b.modified = false
	b.ClearUndoHistory()
	return nil
}

// SaveFile saves the buffer content to a file. The buffer is marked clean
// only when every write, the flush and the close succeed.
func (b *TextBuffer) SaveFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}

	writeErr := b.writeTo(bufio.NewWriter(file))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	b.markClean()
	return nil
}

func (b *TextBuffer) writeTo(writer *bufio.Writer) error {
	for i, line := range b.lines {
		if _, err := writer.WriteString(string(line)); err != nil {
			return err
		}
		if i < len(b.lines)-1 {
			if _, err := writer.WriteString(b.lineEndings); err != nil {
				return err
			}
		}
	}

	if b.trailingNewline {
		if _, err := writer.WriteString(b.lineEndings); err != nil {
			return err
		}
	}

	return writer.Flush()
}

// SetContent sets the buffer content from a string.
func (b *TextBuffer) SetContent(content string) {
	b.setContentInternal(content)
	b.ClearUndoHistory()
	b.markClean()
}

// GetContent returns the buffer content as a string.
func (b *TextBuffer) GetContent() string {
	var builder strings.Builder
	for i, line := range b.lines {
		builder.WriteString(string(line))
		if i < len(b.lines)-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// GetLines returns all lines in the buffer.
func (b *TextBuffer) GetLines() [][]rune {
	return b.lines
}

// GetLine returns a specific line from the buffer.
func (b *TextBuffer) GetLine(row int) []rune {
	if row < 0 || row >= len(b.lines) {
		return []rune{}
	}
	return b.lines[row]
}

// GetLineCount returns the total number of lines in the buffer.
func (b *TextBuffer) GetLineCount() int {
	return len(b.lines)
}

// GetLineLength returns the length of a specific line.
func (b *TextBuffer) GetLineLength(row int) int {
	if row < 0 || row >= len(b.lines) {
		return 0
	}
	return len(b.lines[row])
}

// InsertRune inserts a rune at the specified position.
func (b *TextBuffer) InsertRune(row, col int, r rune) {
	if row < 0 || row >= len(b.lines) {
		return
	}

	line := b.lines[row]
	if col > len(line) {
		col = len(line)
	}

	// Record operation for undo
	b.pushOp(EditOp{
		Type:      OpInsert,
		Row:       row,
		Col:       col,
		Text:      string(r),
		CursorRow: row,
		CursorCol: col + 1,
	})

	newLine := make([]rune, 0, len(line)+1)
	newLine = append(newLine, line[:col]...)
	newLine = append(newLine, r)
	newLine = append(newLine, line[col:]...)
	b.lines[row] = newLine
	b.modified = true
}

// InsertNewline inserts a newline at the specified position.
func (b *TextBuffer) InsertNewline(row, col int) (newRow, newCol int) {
	if row < 0 || row >= len(b.lines) {
		return row, col
	}

	currentLine := b.lines[row]
	if col > len(currentLine) {
		col = len(currentLine)
	}

	// Record operation for undo
	b.pushOp(EditOp{
		Type:      OpNewline,
		Row:       row,
		Col:       col,
		CursorRow: row + 1,
		CursorCol: 0,
	})

	before := currentLine[:col]
	after := currentLine[col:]

	b.lines[row] = before

	newLines := make([][]rune, len(b.lines)+1)
	copy(newLines[:row+1], b.lines[:row+1])
	newLines[row+1] = after
	copy(newLines[row+2:], b.lines[row+1:])
	b.lines = newLines

	b.modified = true
	return row + 1, 0
}

// DeleteCharBackward deletes the character before the cursor position.
func (b *TextBuffer) DeleteCharBackward(row, col int) (newRow, newCol int, deleted bool) {
	if row < 0 || row >= len(b.lines) {
		return row, col, false
	}

	if col > 0 {
		line := b.lines[row]
		if col > len(line) {
			col = len(line)
		}

		// Record operation for undo (delete single char)
		b.pushOp(EditOp{
			Type:      OpDelete,
			Row:       row,
			Col:       col - 1,
			Text:      string(line[col-1]),
			CursorRow: row,
			CursorCol: col - 1,
		})

		newLine := make([]rune, 0, len(line)-1)
		newLine = append(newLine, line[:col-1]...)
		newLine = append(newLine, line[col:]...)
		b.lines[row] = newLine
		b.modified = true
		return row, col - 1, true
	} else if row > 0 {
		prevLine := b.lines[row-1]
		prevLineLen := len(prevLine)

		// Record operation for undo (delete newline - merge lines)
		b.pushOp(EditOp{
			Type:      OpDeleteNewline,
			Row:       row - 1,
			Col:       prevLineLen,
			CursorRow: row - 1,
			CursorCol: prevLineLen,
		})

		currentLine := b.lines[row]
		b.lines[row-1] = append(prevLine, currentLine...)

		newLines := make([][]rune, len(b.lines)-1)
		copy(newLines[:row], b.lines[:row])
		copy(newLines[row:], b.lines[row+1:])
		b.lines = newLines

		b.modified = true
		return row - 1, prevLineLen, true
	}

	return row, col, false
}

// DeleteCharForward deletes the character at the cursor position.
func (b *TextBuffer) DeleteCharForward(row, col int) (deleted bool) {
	if row < 0 || row >= len(b.lines) {
		return false
	}

	line := b.lines[row]
	if col < len(line) {
		// Record operation for undo (delete single char)
		b.pushOp(EditOp{
			Type:      OpDelete,
			Row:       row,
			Col:       col,
			Text:      string(line[col]),
			CursorRow: row,
			CursorCol: col,
		})

		newLine := make([]rune, 0, len(line)-1)
		newLine = append(newLine, line[:col]...)
		newLine = append(newLine, line[col+1:]...)
		b.lines[row] = newLine
		b.modified = true
		return true
	} else if row < len(b.lines)-1 {
		// Record operation for undo (delete newline - merge lines)
		b.pushOp(EditOp{
			Type:      OpDeleteNewline,
			Row:       row,
			Col:       col,
			CursorRow: row,
			CursorCol: col,
		})

		currentLine := b.lines[row]
		nextLine := b.lines[row+1]
		b.lines[row] = append(currentLine, nextLine...)

		newLines := make([][]rune, len(b.lines)-1)
		copy(newLines[:row+1], b.lines[:row+1])
		copy(newLines[row+1:], b.lines[row+2:])
		b.lines = newLines

		b.modified = true
		return true
	}

	return false
}

// DeleteRange deletes text between two positions.
func (b *TextBuffer) DeleteRange(startRow, startCol, endRow, endCol int) {
	if startRow < 0 || startRow >= len(b.lines) || endRow < 0 || endRow >= len(b.lines) {
		return
	}

	// Capture the text being deleted for undo
	deletedText := b.GetTextRange(startRow, startCol, endRow, endCol)

	// Record operation for undo
	b.pushOp(EditOp{
		Type:      OpDelete,
		Row:       startRow,
		Col:       startCol,
		Text:      deletedText,
		CursorRow: startRow,
		CursorCol: startCol,
	})

	if startRow == endRow {
		line := b.lines[startRow]
		if startCol > len(line) {
			startCol = len(line)
		}
		if endCol > len(line) {
			endCol = len(line)
		}
		newLine := make([]rune, 0, len(line)-(endCol-startCol))
		newLine = append(newLine, line[:startCol]...)
		newLine = append(newLine, line[endCol:]...)
		b.lines[startRow] = newLine
	} else {
		startLine := b.lines[startRow]
		endLine := b.lines[endRow]

		if startCol > len(startLine) {
			startCol = len(startLine)
		}
		if endCol > len(endLine) {
			endCol = len(endLine)
		}

		newLine := make([]rune, 0, len(startLine[:startCol])+len(endLine[endCol:]))
		newLine = append(newLine, startLine[:startCol]...)
		newLine = append(newLine, endLine[endCol:]...)

		linesToDelete := endRow - startRow
		newLines := make([][]rune, len(b.lines)-linesToDelete)
		copy(newLines[:startRow], b.lines[:startRow])
		newLines[startRow] = newLine
		copy(newLines[startRow+1:], b.lines[endRow+1:])
		b.lines = newLines
	}

	b.modified = true
}

// InsertText inserts text at the specified position.
func (b *TextBuffer) InsertText(row, col int, text string) (newRow, newCol int) {
	if row < 0 || row >= len(b.lines) {
		return row, col
	}

	currentLine := b.lines[row]
	if col > len(currentLine) {
		col = len(currentLine)
	}

	lines := strings.Split(text, "\n")
	finalRow := row + len(lines) - 1
	finalCol := len([]rune(lines[len(lines)-1]))
	if len(lines) == 1 {
		finalCol = col + len([]rune(text))
	}

	// Record operation for undo
	b.pushOp(EditOp{
		Type:      OpInsert,
		Row:       row,
		Col:       col,
		Text:      text,
		CursorRow: finalRow,
		CursorCol: finalCol,
	})

	if len(lines) == 1 {
		textRunes := []rune(text)
		newLine := make([]rune, 0, len(currentLine)+len(textRunes))
		newLine = append(newLine, currentLine[:col]...)
		newLine = append(newLine, textRunes...)
		newLine = append(newLine, currentLine[col:]...)
		b.lines[row] = newLine
		b.modified = true
		return row, col + len(textRunes)
	}

	before := make([]rune, col)
	copy(before, currentLine[:col])
	after := make([]rune, len(currentLine)-col)
	copy(after, currentLine[col:])

	newLines := make([][]rune, len(b.lines)+len(lines)-1)
	copy(newLines[:row], b.lines[:row])

	firstLine := make([]rune, 0, len(before)+len(lines[0]))
	firstLine = append(firstLine, before...)
	firstLine = append(firstLine, []rune(lines[0])...)
	newLines[row] = firstLine

	for i := 1; i < len(lines)-1; i++ {
		newLines[row+i] = []rune(lines[i])
	}

	lastLineIndex := row + len(lines) - 1
	if lastLineIndex != row {
		lastLine := make([]rune, 0, len(lines[len(lines)-1])+len(after))
		lastLine = append(lastLine, []rune(lines[len(lines)-1])...)
		lastLine = append(lastLine, after...)
		newLines[lastLineIndex] = lastLine
	}

	copy(newLines[row+len(lines):], b.lines[row+1:])
	b.lines = newLines

	b.modified = true
	return row + len(lines) - 1, len([]rune(lines[len(lines)-1]))
}

// IsModified returns true if the buffer has been modified.
func (b *TextBuffer) IsModified() bool {
	return b.modified
}

// SetModified sets the modified state of the buffer.
func (b *TextBuffer) SetModified(modified bool) {
	b.modified = modified
}

// GetTabSize returns the tab size setting.
func (b *TextBuffer) GetTabSize() int {
	return b.tabSize
}

// SetTabSize sets the tab size setting.
func (b *TextBuffer) SetTabSize(size int) {
	if size > 0 {
		b.tabSize = size
	}
}

func (b *TextBuffer) detectLineEndingsFromContent(content string) {
	if strings.Contains(content, "\r\n") {
		b.lineEndings = "\r\n"
	} else if strings.Contains(content, "\r") {
		b.lineEndings = "\r"
	} else {
		b.lineEndings = "\n"
	}
}

func (b *TextBuffer) setContentInternal(content string) {
	b.trailingNewline = len(content) > 0 && (content[len(content)-1] == '\n' || content[len(content)-1] == '\r')
	b.detectLineEndingsFromContent(content)

	content = strings.TrimSuffix(content, "\n")
	content = strings.TrimSuffix(content, "\r")

	if content == "" {
		b.lines = [][]rune{{}}
		return
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	splitLines := strings.Split(normalized, "\n")
	b.lines = make([][]rune, len(splitLines))
	for i, line := range splitLines {
		b.lines[i] = []rune(line)
	}
}

// GetTextRange returns text content between two positions.
func (b *TextBuffer) GetTextRange(startRow, startCol, endRow, endCol int) string {
	if startRow < 0 || startRow >= len(b.lines) || endRow < 0 || endRow >= len(b.lines) {
		return ""
	}

	if startRow == endRow {
		line := b.lines[startRow]
		if startCol > len(line) {
			startCol = len(line)
		}
		if endCol > len(line) {
			endCol = len(line)
		}
		return string(line[startCol:endCol])
	}

	var result strings.Builder

	startLine := b.lines[startRow]
	if startCol > len(startLine) {
		startCol = len(startLine)
	}
	result.WriteString(string(startLine[startCol:]))
	result.WriteString("\n")

	for i := startRow + 1; i < endRow; i++ {
		result.WriteString(string(b.lines[i]))
		result.WriteString("\n")
	}

	endLine := b.lines[endRow]
	if endCol > len(endLine) {
		endCol = len(endLine)
	}
	result.WriteString(string(endLine[:endCol]))

	return result.String()
}

// pushOp adds an edit operation to the undo stack
func (b *TextBuffer) pushOp(op EditOp) {
	if !b.recordingUndo {
		return
	}

	op.ID = b.nextOpID
	b.nextOpID++
	op.Timestamp = time.Now()

	// Check if we can coalesce with previous operation
	if len(b.undoStack) > 0 {
		last := &b.undoStack[len(b.undoStack)-1]
		if b.canCoalesce(last, &op) {
			// Coalesce: extend the previous operation
			if op.Type == OpInsert {
				last.Text += op.Text
			} else if op.Type == OpDelete {
				// Prepend for backspace (deleting backwards)
				last.Text = op.Text + last.Text
				last.Col = op.Col
			}
			last.CursorRow = op.CursorRow
			last.CursorCol = op.CursorCol
			last.Timestamp = op.Timestamp
			last.ID = op.ID
			b.lastOpTime = op.Timestamp
			return
		}
	}

	b.undoStack = append(b.undoStack, op)
	b.lastOpTime = op.Timestamp

	// Limit undo stack size
	if len(b.undoStack) > b.maxUndoSize {
		b.undoStack = b.undoStack[1:]
		if b.savePointIdx >= 0 {
			if b.savePointIdx <= 1 {
				b.savePointIdx = -1
				b.savePointID = 0
			} else {
				b.savePointIdx--
			}
		}
	}

	// Clear redo stack on new edit
	b.redoStack = b.redoStack[:0]
}

// canCoalesce checks if two operations can be merged
func (b *TextBuffer) canCoalesce(last, new *EditOp) bool {
	// Must be same type
	if last.Type != new.Type {
		return false
	}

	// Only coalesce single-char inserts/deletes
	if len(new.Text) != 1 {
		return false
	}

	// Must be within time window
	if new.Timestamp.Sub(last.Timestamp) > b.coalesceDelay {
		return false
	}

	// Must be consecutive
	switch new.Type {
	case OpInsert:
		// Typing: new char should be right after last insert
		expectedCol := last.Col + len([]rune(last.Text))
		return new.Row == last.Row && new.Col == expectedCol
	case OpDelete:
		// Backspace: new delete should be one position before
		return new.Row == last.Row && new.Col == last.Col-1
	}

	return false
}

// SaveState is kept for backwards compatibility but does nothing now.
// Operations are recorded automatically by edit methods.
func (b *TextBuffer) SaveState(cursorRow, cursorCol int) {
	// No-op: operations are now recorded by individual edit methods
}

// SetSearchMatches sets the search matches for the buffer.
func (b *TextBuffer) SetSearchMatches(matches []SearchMatch) {
	b.searchMatches = matches
	b.currentMatch = 0
}

// SetSearchPattern sets the search pattern and regex for replacements.
func (b *TextBuffer) SetSearchPattern(pattern string, regex *regexp.Regexp) {
	b.searchPattern = pattern
	b.searchRegex = regex
}

// GetSearchMatches returns the current search matches.
func (b *TextBuffer) GetSearchMatches() []SearchMatch {
	return b.searchMatches
}

// GetCurrentMatch returns the current match index and total matches.
func (b *TextBuffer) GetCurrentMatch() (int, int) {
	if len(b.searchMatches) == 0 {
		return 0, 0
	}
	return b.currentMatch + 1, len(b.searchMatches)
}

// NextMatch moves to the next search match.
func (b *TextBuffer) NextMatch() (row, col int, found bool) {
	if len(b.searchMatches) == 0 {
		return 0, 0, false
	}

	b.currentMatch = (b.currentMatch + 1) % len(b.searchMatches)
	match := b.searchMatches[b.currentMatch]
	return match.StartRow, match.StartCol, true
}

// PreviousMatch moves to the previous search match.
func (b *TextBuffer) PreviousMatch() (row, col int, found bool) {
	if len(b.searchMatches) == 0 {
		return 0, 0, false
	}

	b.currentMatch--
	if b.currentMatch < 0 {
		b.currentMatch = len(b.searchMatches) - 1
	}
	match := b.searchMatches[b.currentMatch]
	return match.StartRow, match.StartCol, true
}

// ClearSearchMatches clears all search matches.
func (b *TextBuffer) ClearSearchMatches() {
	b.searchMatches = nil
	b.currentMatch = 0
	b.searchPattern = ""
	b.searchRegex = nil
}

// ReplaceCurrentMatch replaces the current search match with replacement text.
func (b *TextBuffer) ReplaceCurrentMatch(replacement string) bool {
	if len(b.searchMatches) == 0 || b.currentMatch >= len(b.searchMatches) {
		return false
	}

	match := b.searchMatches[b.currentMatch]
	if match.StartRow != match.EndRow {
		return false
	}

	b.SaveState(match.StartRow, match.StartCol)

	actualReplacement := replacement
	if b.searchRegex != nil {
		matchedText := string(b.lines[match.StartRow][match.StartCol:match.EndCol])
		actualReplacement = b.searchRegex.ReplaceAllString(matchedText, replacement)
	}

	b.DeleteRange(match.StartRow, match.StartCol, match.EndRow, match.EndCol)
	b.InsertText(match.StartRow, match.StartCol, actualReplacement)

	replacementLen := len([]rune(actualReplacement))
	matchLen := match.EndCol - match.StartCol
	adjustment := replacementLen - matchLen

	for i := b.currentMatch + 1; i < len(b.searchMatches); i++ {
		if b.searchMatches[i].StartRow == match.StartRow {
			b.searchMatches[i].StartCol += adjustment
			b.searchMatches[i].EndCol += adjustment
		}
	}

	b.searchMatches = append(b.searchMatches[:b.currentMatch], b.searchMatches[b.currentMatch+1:]...)
	if b.currentMatch >= len(b.searchMatches) && len(b.searchMatches) > 0 {
		b.currentMatch = len(b.searchMatches) - 1
	}

	return true
}

// ReplaceAllMatches replaces all search matches with replacement text.
func (b *TextBuffer) ReplaceAllMatches(replacement string) int {
	if len(b.searchMatches) == 0 {
		return 0
	}

	b.SaveState(0, 0)

	count := 0

	for i := len(b.searchMatches) - 1; i >= 0; i-- {
		match := b.searchMatches[i]
		if match.StartRow != match.EndRow {
			continue
		}

		actualReplacement := replacement
		if b.searchRegex != nil {
			matchedText := string(b.lines[match.StartRow][match.StartCol:match.EndCol])
			actualReplacement = b.searchRegex.ReplaceAllString(matchedText, replacement)
		}

		b.DeleteRange(match.StartRow, match.StartCol, match.EndRow, match.EndCol)
		b.InsertText(match.StartRow, match.StartCol, actualReplacement)
		count++
	}

	b.searchMatches = nil
	b.currentMatch = 0

	return count
}

// Undo reverts the last operation.
func (b *TextBuffer) Undo() (cursorX, cursorY int, success bool) {
	if len(b.undoStack) == 0 {
		return 0, 0, false
	}

	// Pop from undo stack
	lastIdx := len(b.undoStack) - 1
	op := b.undoStack[lastIdx]
	b.undoStack[lastIdx] = EditOp{}
	b.undoStack = b.undoStack[:lastIdx]

	// Apply reverse operation without recording
	b.recordingUndo = false
	cursorX, cursorY = b.applyReverseOp(op)
	b.recordingUndo = true

	// Push to redo stack
	b.redoStack = append(b.redoStack, op)

	// Update modified state
	b.modified = !b.isAtSavePoint()

	return cursorX, cursorY, true
}

// Redo reapplies a previously undone operation.
func (b *TextBuffer) Redo() (cursorX, cursorY int, success bool) {
	if len(b.redoStack) == 0 {
		return 0, 0, false
	}

	// Pop from redo stack
	lastIdx := len(b.redoStack) - 1
	op := b.redoStack[lastIdx]
	b.redoStack[lastIdx] = EditOp{}
	b.redoStack = b.redoStack[:lastIdx]

	// Re-apply operation without recording
	b.recordingUndo = false
	cursorX, cursorY = b.applyOp(op)
	b.recordingUndo = true

	// Push back to undo stack
	b.undoStack = append(b.undoStack, op)

	// Update modified state
	b.modified = !b.isAtSavePoint()

	return cursorX, cursorY, true
}

// applyOp applies an operation and returns the resulting cursor position
func (b *TextBuffer) applyOp(op EditOp) (cursorX, cursorY int) {
	switch op.Type {
	case OpInsert:
		b.insertTextRaw(op.Row, op.Col, op.Text)
		return op.CursorCol, op.CursorRow
	case OpDelete:
		b.deleteTextRaw(op.Row, op.Col, op.Text)
		return op.Col, op.Row
	case OpNewline:
		b.insertNewlineRaw(op.Row, op.Col)
		return 0, op.Row + 1
	case OpDeleteNewline:
		b.deleteNewlineRaw(op.Row, op.Col)
		return op.Col, op.Row
	}
	return op.CursorCol, op.CursorRow
}

// applyReverseOp applies the reverse of an operation
func (b *TextBuffer) applyReverseOp(op EditOp) (cursorX, cursorY int) {
	switch op.Type {
	case OpInsert:
		// Reverse of insert is delete
		b.deleteTextRaw(op.Row, op.Col, op.Text)
		return op.Col, op.Row
	case OpDelete:
		// Reverse of delete is insert
		b.insertTextRaw(op.Row, op.Col, op.Text)
		// Position cursor after inserted text
		lines := strings.Split(op.Text, "\n")
		if len(lines) == 1 {
			return op.Col + len([]rune(op.Text)), op.Row
		}
		return len([]rune(lines[len(lines)-1])), op.Row + len(lines) - 1
	case OpNewline:
		// Reverse of inserting newline is deleting it
		b.deleteNewlineRaw(op.Row, op.Col)
		return op.Col, op.Row
	case OpDeleteNewline:
		// Reverse of deleting newline is inserting it
		b.insertNewlineRaw(op.Row, op.Col)
		return 0, op.Row + 1
	}
	return op.Col, op.Row
}

// insertTextRaw inserts text without recording undo
func (b *TextBuffer) insertTextRaw(row, col int, text string) {
	if row < 0 || row >= len(b.lines) {
		return
	}

	currentLine := b.lines[row]
	if col > len(currentLine) {
		col = len(currentLine)
	}

	lines := strings.Split(text, "\n")
	if len(lines) == 1 {
		textRunes := []rune(text)
		newLine := make([]rune, 0, len(currentLine)+len(textRunes))
		newLine = append(newLine, currentLine[:col]...)
		newLine = append(newLine, textRunes...)
		newLine = append(newLine, currentLine[col:]...)
		b.lines[row] = newLine
		return
	}

	before := make([]rune, col)
	copy(before, currentLine[:col])
	after := make([]rune, len(currentLine)-col)
	copy(after, currentLine[col:])

	newLines := make([][]rune, len(b.lines)+len(lines)-1)
	copy(newLines[:row], b.lines[:row])

	firstLine := make([]rune, 0, len(before)+len(lines[0]))
	firstLine = append(firstLine, before...)
	firstLine = append(firstLine, []rune(lines[0])...)
	newLines[row] = firstLine

	for i := 1; i < len(lines)-1; i++ {
		newLines[row+i] = []rune(lines[i])
	}

	lastLineIndex := row + len(lines) - 1
	if lastLineIndex != row {
		lastLine := make([]rune, 0, len(lines[len(lines)-1])+len(after))
		lastLine = append(lastLine, []rune(lines[len(lines)-1])...)
		lastLine = append(lastLine, after...)
		newLines[lastLineIndex] = lastLine
	}

	copy(newLines[row+len(lines):], b.lines[row+1:])
	b.lines = newLines
}

// deleteTextRaw deletes text without recording undo
func (b *TextBuffer) deleteTextRaw(row, col int, text string) {
	if row < 0 || row >= len(b.lines) {
		return
	}

	lines := strings.Split(text, "\n")
	if len(lines) == 1 {
		line := b.lines[row]
		endCol := col + len([]rune(text))
		if endCol > len(line) {
			endCol = len(line)
		}
		newLine := make([]rune, 0, len(line)-(endCol-col))
		newLine = append(newLine, line[:col]...)
		newLine = append(newLine, line[endCol:]...)
		b.lines[row] = newLine
		return
	}

	// Multi-line delete
	endRow := row + len(lines) - 1
	endCol := len([]rune(lines[len(lines)-1]))

	if endRow >= len(b.lines) {
		endRow = len(b.lines) - 1
		endCol = len(b.lines[endRow])
	}

	startLine := b.lines[row]
	endLine := b.lines[endRow]

	if col > len(startLine) {
		col = len(startLine)
	}
	if endCol > len(endLine) {
		endCol = len(endLine)
	}

	newLine := make([]rune, 0, col+len(endLine)-endCol)
	newLine = append(newLine, startLine[:col]...)
	newLine = append(newLine, endLine[endCol:]...)

	linesToDelete := endRow - row
	newLines := make([][]rune, len(b.lines)-linesToDelete)
	copy(newLines[:row], b.lines[:row])
	newLines[row] = newLine
	copy(newLines[row+1:], b.lines[endRow+1:])
	b.lines = newLines
}

// insertNewlineRaw inserts a newline without recording undo
func (b *TextBuffer) insertNewlineRaw(row, col int) {
	if row < 0 || row >= len(b.lines) {
		return
	}

	currentLine := b.lines[row]
	if col > len(currentLine) {
		col = len(currentLine)
	}

	before := currentLine[:col]
	after := currentLine[col:]

	b.lines[row] = before

	newLines := make([][]rune, len(b.lines)+1)
	copy(newLines[:row+1], b.lines[:row+1])
	newLines[row+1] = after
	copy(newLines[row+2:], b.lines[row+1:])
	b.lines = newLines
}

// deleteNewlineRaw deletes a newline (merges two lines) without recording undo
func (b *TextBuffer) deleteNewlineRaw(row, col int) {
	if row < 0 || row >= len(b.lines)-1 {
		return
	}

	currentLine := b.lines[row]
	nextLine := b.lines[row+1]
	b.lines[row] = append(currentLine, nextLine...)

	newLines := make([][]rune, len(b.lines)-1)
	copy(newLines[:row+1], b.lines[:row+1])
	copy(newLines[row+1:], b.lines[row+2:])
	b.lines = newLines
}

// CanUndo returns true if undo is available.
func (b *TextBuffer) CanUndo() bool {
	return len(b.undoStack) > 0
}

// CanRedo returns true if redo is available.
func (b *TextBuffer) CanRedo() bool {
	return len(b.redoStack) > 0
}

// ClearUndoHistory clears both undo and redo stacks.
func (b *TextBuffer) ClearUndoHistory() {
	b.undoStack = b.undoStack[:0]
	b.redoStack = b.redoStack[:0]
	b.savePointIdx = 0
	b.savePointID = 0
	b.nextOpID = 1
}

func (b *TextBuffer) markClean() {
	b.modified = false
	b.savePointIdx = len(b.undoStack)
	if b.savePointIdx > 0 {
		b.savePointID = b.undoStack[b.savePointIdx-1].ID
		b.undoStack[b.savePointIdx-1].Timestamp = time.Time{}
	} else {
		b.savePointID = 0
	}
}

func (b *TextBuffer) isAtSavePoint() bool {
	if b.savePointIdx < 0 {
		return false
	}
	if len(b.undoStack) != b.savePointIdx {
		return false
	}
	if b.savePointIdx == 0 {
		return true
	}
	if b.savePointIdx-1 >= len(b.undoStack) {
		return false
	}
	return b.undoStack[b.savePointIdx-1].ID == b.savePointID
}

// SetLine replaces the content of a specific line
func (b *TextBuffer) SetLine(row int, content []rune) {
	if row >= 0 && row < len(b.lines) {
		b.lines[row] = content
		b.modified = true
	}
}

// ClearTokenCache clears the syntax highlighting token cache
func (b *TextBuffer) ClearTokenCache() {
	// Token cache is handled in the CodeEditor, not the buffer
	// This is a no-op for now
}
