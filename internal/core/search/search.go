// Package search provides text search and replace functionality for buffers.
package search

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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

// nonWordClass matches one rune that isWordRune rejects. Go's \b is ASCII only,
// so whole word regex search uses this class instead.
const nonWordClass = `[^\p{L}\p{Nd}_]`

func searchRegex(lines [][]rune, pattern string, options SearchOptions) []Match {
	flags := ""
	if !options.CaseSensitive {
		flags = "(?i)"
	}

	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil
	}

	if options.WholeWord {
		// The pattern compiled alone, so it is balanced and safe to wrap. The
		// trailing group requires a word boundary after the match, which lets
		// the engine pick an alternative or a repetition that ends on one.
		re = regexp.MustCompile(flags + `(` + pattern + `)(?:$|` + nonWordClass + `)`)
	}

	// Index of the submatch that holds the match itself: the whole match, or
	// the first group when the trailing boundary is part of the regex.
	group := 0
	if options.WholeWord {
		group = 1
	}

	var matches []Match
	for row, line := range lines {
		lineStr := string(line)

		for _, loc := range re.FindAllStringSubmatchIndex(lineStr, -1) {
			start, end := loc[2*group], loc[2*group+1]
			startCol := utf8.RuneCountInString(lineStr[:start])
			endCol := startCol + utf8.RuneCountInString(lineStr[start:end])
			if options.WholeWord && !isWordBoundary(line, startCol, endCol) {
				continue
			}

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
	if start > 0 && isWordRune(line[start-1]) {
		return false
	}
	if end < len(line) && isWordRune(line[end]) {
		return false
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
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
