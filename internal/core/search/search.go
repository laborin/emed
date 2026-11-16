// Package search provides text search and replace functionality for buffers.
package search

import (
	"regexp"
	"strings"
	"unicode"
)

// Match represents a single search match in the buffer.
type Match struct {
	StartRow int
	StartCol int
	EndRow   int
	EndCol   int
}

// SearchOptions contains options for search operations.
type SearchOptions struct {
	CaseSensitive bool
	WholeWord     bool
	UseRegex      bool
}

// Search finds all matches of the given pattern in the buffer lines.
func Search(lines [][]rune, pattern string, options SearchOptions) []Match {
	if pattern == "" {
		return nil
	}

	var matches []Match

	if options.UseRegex {
		matches = searchRegex(lines, pattern, options)
	} else {
		matches = searchPlain(lines, pattern, options)
	}

	return matches
}

func searchPlain(lines [][]rune, pattern string, options SearchOptions) []Match {
	var matches []Match
	searchPattern := pattern

	if !options.CaseSensitive {
		searchPattern = strings.ToLower(pattern)
	}

	patternRunes := []rune(searchPattern)
	patternLen := len(patternRunes)

	for row, line := range lines {
		lineStr := string(line)
		if !options.CaseSensitive {
			lineStr = strings.ToLower(lineStr)
		}
		lineRunes := []rune(lineStr)

		for col := 0; col <= len(lineRunes)-patternLen; col++ {
			if matchesAt(lineRunes, patternRunes, col) {
				if options.WholeWord {
					if !isWordBoundary(lineRunes, col, col+patternLen) {
						continue
					}
				}
				matches = append(matches, Match{
					StartRow: row,
					StartCol: col,
					EndRow:   row,
					EndCol:   col + patternLen,
				})
			}
		}
	}

	return matches
}

func searchRegex(lines [][]rune, pattern string, options SearchOptions) []Match {
	var matches []Match

	flags := ""
	if !options.CaseSensitive {
		flags = "(?i)"
	}

	if options.WholeWord {
		pattern = `\b` + pattern + `\b`
	}

	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil
	}

	for row, line := range lines {
		lineStr := string(line)
		allMatches := re.FindAllStringIndex(lineStr, -1)

		for _, match := range allMatches {
			startCol := len([]rune(lineStr[:match[0]]))
			endCol := len([]rune(lineStr[:match[1]]))

			matches = append(matches, Match{
				StartRow: row,
				StartCol: startCol,
				EndRow:   row,
				EndCol:   endCol,
			})
		}
	}

	return matches
}

func matchesAt(line, pattern []rune, pos int) bool {
	if pos+len(pattern) > len(line) {
		return false
	}

	for i, r := range pattern {
		if line[pos+i] != r {
			return false
		}
	}

	return true
}

func isWordBoundary(line []rune, start, end int) bool {
	if start > 0 {
		prevChar := line[start-1]
		if unicode.IsLetter(prevChar) || unicode.IsDigit(prevChar) || prevChar == '_' {
			return false
		}
	}

	if end < len(line) {
		nextChar := line[end]
		if unicode.IsLetter(nextChar) || unicode.IsDigit(nextChar) || nextChar == '_' {
			return false
		}
	}

	return true
}

// Replace replaces text at the given match with replacement text.
func Replace(lines [][]rune, match Match, replacement string) [][]rune {
	if match.StartRow != match.EndRow {
		return lines
	}

	row := match.StartRow
	if row < 0 || row >= len(lines) {
		return lines
	}

	line := lines[row]
	if !validColumns(match.StartCol, match.EndCol, len(line)) {
		return lines
	}
	replacementRunes := []rune(replacement)

	newLine := make([]rune, 0, len(line)-match.EndCol+match.StartCol+len(replacementRunes))
	newLine = append(newLine, line[:match.StartCol]...)
	newLine = append(newLine, replacementRunes...)
	newLine = append(newLine, line[match.EndCol:]...)

	newLines := make([][]rune, len(lines))
	copy(newLines, lines)
	newLines[row] = newLine

	return newLines
}

// ReplaceAll replaces all matches with the replacement text.
func ReplaceAll(lines [][]rune, matches []Match, replacement string) [][]rune {
	if len(matches) == 0 {
		return lines
	}

	newLines := make([][]rune, len(lines))
	copy(newLines, lines)

	replacementRunes := []rune(replacement)
	offset := make([]int, len(lines))

	for _, match := range matches {
		if match.StartRow != match.EndRow {
			continue
		}

		row := match.StartRow
		if row < 0 || row >= len(newLines) {
			continue
		}

		if !validColumns(match.StartCol, match.EndCol, len(lines[row])) {
			continue
		}

		line := newLines[row]
		matchLen := match.EndCol - match.StartCol
		adjustedStart := match.StartCol + offset[row]
		adjustedEnd := adjustedStart + matchLen

		if !validColumns(adjustedStart, adjustedEnd, len(line)) {
			continue
		}

		newLine := make([]rune, 0, len(line)-matchLen+len(replacementRunes))
		newLine = append(newLine, line[:adjustedStart]...)
		newLine = append(newLine, replacementRunes...)
		newLine = append(newLine, line[adjustedEnd:]...)

		newLines[row] = newLine
		offset[row] += len(replacementRunes) - matchLen
	}

	return newLines
}

func validColumns(start, end, length int) bool {
	return start >= 0 && start <= end && end <= length
}
