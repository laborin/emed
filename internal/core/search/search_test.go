package search

import (
	"slices"
	"testing"
)

func TestSearch(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		pattern string
		options SearchOptions
		want    []Match
	}{
		{
			name:    "plain case folding and rune columns",
			lines:   []string{"猫 café CAFÉ", "Caféine"},
			pattern: "CAFÉ",
			want: []Match{
				{StartRow: 0, StartCol: 2, EndRow: 0, EndCol: 6},
				{StartRow: 0, StartCol: 7, EndRow: 0, EndCol: 11},
				{StartRow: 1, StartCol: 0, EndRow: 1, EndCol: 4},
			},
		},
		{
			name:    "case sensitive",
			lines:   []string{"lower LOWER"},
			pattern: "LOWER",
			options: SearchOptions{CaseSensitive: true},
			want:    []Match{{StartCol: 6, EndCol: 11}},
		},
		{
			name:    "whole words exclude Unicode letters digits and underscores",
			lines:   []string{"go 2go _go go日 (go)"},
			pattern: "go",
			options: SearchOptions{WholeWord: true},
			want:    []Match{{EndCol: 2}, {StartCol: 16, EndCol: 18}},
		},
		{
			name:    "whole word regex and rune columns",
			lines:   []string{"é ID12 xID34 ID56_ ID78"},
			pattern: "id[0-9]+",
			options: SearchOptions{UseRegex: true, WholeWord: true},
			want:    []Match{{StartCol: 2, EndCol: 6}, {StartCol: 19, EndCol: 23}},
		},
		{
			name:    "invalid regex",
			lines:   []string{"abc"},
			pattern: "[",
			options: SearchOptions{UseRegex: true},
		},
		{
			name:  "empty pattern",
			lines: []string{"abc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Search(toRuneLines(tt.lines), tt.pattern, tt.options)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Search() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReplace(t *testing.T) {
	original := []string{"é猫!", "other"}
	lines := toRuneLines(original)
	tests := []struct {
		name  string
		match Match
		want  []string
	}{
		{"replace Unicode by rune column", Match{StartCol: 1, EndCol: 2}, []string{"é字🙂!", "other"}},
		{"ignore a cross-line span", Match{EndRow: 1, EndCol: 1}, original},
		{"ignore an out-of-range row", Match{StartRow: 2, EndRow: 2}, original},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLines(t, Replace(lines, tt.match, "字🙂"), tt.want)
			assertLines(t, lines, original)
		})
	}
}

func TestReplaceAll(t *testing.T) {
	tests := []struct {
		name        string
		pattern     string
		replacement string
		want        []string
	}{
		{"grow matches on each row", "猫", "fish", []string{"fish fish", "untouched", "é fish fish!"}},
		{"shrink matches on each row", "猫", "", []string{" ", "untouched", "é  !"}},
		{"no matches", "dog", "fish", []string{"猫 猫", "untouched", "é 猫 猫!"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := []string{"猫 猫", "untouched", "é 猫 猫!"}
			lines := toRuneLines(original)
			matches := Search(lines, tt.pattern, SearchOptions{})
			assertLines(t, ReplaceAll(lines, matches, tt.replacement), tt.want)
			assertLines(t, lines, original)
		})
	}
}

func toRuneLines(lines []string) [][]rune {
	result := make([][]rune, len(lines))
	for i, line := range lines {
		result[i] = []rune(line)
	}
	return result
}

func assertLines(t *testing.T, got [][]rune, want []string) {
	t.Helper()
	actual := make([]string, len(got))
	for i, line := range got {
		actual[i] = string(line)
	}
	if !slices.Equal(actual, want) {
		t.Errorf("lines = %q, want %q", actual, want)
	}
}
