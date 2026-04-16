package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

const anchorContextRadius = 2

// getAnchorText returns the content of a specific line (0-based index) in a file.
func getAnchorText(projectDir, relPath string, line int) string {
	lines := readFileLines(projectDir, relPath)
	if lines == nil || line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

// getAnchorContext returns contextRadius lines above and below (inclusive) the given 0-based index.
func getAnchorContext(lines []string, idx int) []string {
	start := max(0, idx-anchorContextRadius)
	end := min(len(lines), idx+anchorContextRadius+1)
	context := make([]string, end-start)
	copy(context, lines[start:end])
	return context
}

// findAnchorLine tries to locate anchor text in the given lines.
// Returns (new 0-based index, orphaned).
func findAnchorLine(lines []string, currentIdx int, anchorText string, anchorContext []string) (int, bool) {
	if lines == nil {
		return currentIdx, true
	}

	// 1. Check if anchor text is still at the current line
	if currentIdx >= 0 && currentIdx < len(lines) && lines[currentIdx] == anchorText {
		return currentIdx, false
	}

	// 2. Search for exact match anywhere
	for i, line := range lines {
		if line == anchorText {
			return i, false
		}
	}

	// 3. Fuzzy match using context
	if len(anchorContext) > 0 {
		bestScore := 0
		bestLine := -1

		for i := range lines {
			score := contextMatchScore(lines, i, anchorContext)
			if score > bestScore {
				bestScore = score
				bestLine = i
			}
		}

		if bestLine >= 0 && bestScore >= len(anchorContext)/2 {
			return bestLine, false
		}
	}

	// 4. No match — orphaned
	return currentIdx, true
}

// contextMatchScore returns how many context lines match around position idx.
func contextMatchScore(lines []string, idx int, context []string) int {
	score := 0
	contextStart := idx - anchorContextRadius
	for i, ctxLine := range context {
		fileIdx := contextStart + i
		if fileIdx >= 0 && fileIdx < len(lines) {
			if strings.TrimSpace(lines[fileIdx]) == strings.TrimSpace(ctxLine) {
				score++
			}
		}
	}
	return score
}

// readFileLines reads a file and returns its lines.
func readFileLines(projectDir, relPath string) []string {
	absPath := filepath.Join(projectDir, relPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}
