// Package anchor provides line-anchoring logic for comments and tours.
// Anchors can be re-evaluated when file content changes, finding the
// new line position by exact match, search, or fuzzy context matching.
package anchor

import (
	"strings"

	"github.com/pairpad/pairpad/internal/protocol"
)

const ContextRadius = 2

// GetContext returns ContextRadius lines above and below (inclusive) the given 0-based index.
func GetContext(lines []string, idx int) []string {
	start := max(0, idx-ContextRadius)
	end := min(len(lines), idx+ContextRadius+1)
	context := make([]string, end-start)
	copy(context, lines[start:end])
	return context
}

// FindLine tries to locate anchor text in the given lines.
// Returns (new 0-based index, orphaned, confidence).
// Confidence is 1.0 for exact matches, 0.0 for orphaned, and
// proportional to context line matches for fuzzy matches.
func FindLine(lines []string, currentIdx int, anchorText string, anchorContext []string) (int, bool, float32) {
	if lines == nil {
		return currentIdx, true, 0
	}

	// 1. Check if anchor text is still at the current line
	if currentIdx >= 0 && currentIdx < len(lines) && lines[currentIdx] == anchorText {
		return currentIdx, false, 1.0
	}

	// 2. Search for exact match anywhere
	for i, line := range lines {
		if line == anchorText {
			return i, false, 1.0
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
			confidence := float32(bestScore) / float32(len(anchorContext))
			return bestLine, false, confidence
		}
	}

	// 4. No match — orphaned
	return currentIdx, true, 0
}

func contextMatchScore(lines []string, idx int, context []string) int {
	score := 0
	contextStart := idx - ContextRadius
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

// PopulateComment fills in anchor text and context for a comment.
func PopulateComment(c *protocol.Comment, lines []string) {
	if c.ParentID != "" || lines == nil {
		return
	}
	if c.Line >= 1 && c.Line <= len(lines) {
		c.AnchorText = lines[c.Line-1]
		c.AnchorContext = GetContext(lines, c.Line-1)
	}
	if c.LineEnd > c.Line && c.LineEnd <= len(lines) {
		c.AnchorTextEnd = lines[c.LineEnd-1]
		c.AnchorContextEnd = GetContext(lines, c.LineEnd-1)
	}
}

// PopulateTourStep fills in anchor text and context for a tour step.
func PopulateTourStep(step *protocol.TourStep, lines []string) {
	if lines == nil {
		return
	}
	if step.Line >= 1 && step.Line <= len(lines) {
		step.AnchorText = lines[step.Line-1]
		step.AnchorContext = GetContext(lines, step.Line-1)
	}
	if step.LineEnd > step.Line && step.LineEnd <= len(lines) {
		step.AnchorTextEnd = lines[step.LineEnd-1]
		step.AnchorContextEnd = GetContext(lines, step.LineEnd-1)
	}
}

const staleThreshold = 0.8

// ReanchorComments re-anchors all comments for a file. Returns true if any changed.
func ReanchorComments(comments []protocol.Comment, file string, lines []string) bool {
	changed := false
	for i := range comments {
		c := &comments[i]
		if c.File != file || c.ParentID != "" || c.AnchorText == "" {
			continue
		}

		newIdx, orphaned, confidence := FindLine(lines, c.Line-1, c.AnchorText, c.AnchorContext)
		newLine := newIdx + 1

		// Re-anchor end line
		newEndIdx := -1
		endOrphaned := false
		endConfidence := float32(1.0)
		if c.LineEnd > 0 && c.AnchorTextEnd != "" {
			newEndIdx, endOrphaned, endConfidence = FindLine(lines, c.LineEnd-1, c.AnchorTextEnd, c.AnchorContextEnd)
		}

		if (orphaned || endOrphaned) && !c.Orphaned {
			c.Orphaned = true
			c.Stale = false
			changed = true
		} else if !orphaned && !endOrphaned {
			if c.Orphaned {
				c.Orphaned = false
				changed = true
			}
			stale := confidence < staleThreshold || endConfidence < staleThreshold
			if c.Stale != stale {
				c.Stale = stale
				changed = true
			}
			if c.Line != newLine {
				c.Line = newLine
				c.AnchorContext = GetContext(lines, newIdx)
				changed = true
			}
			if newEndIdx >= 0 {
				newLineEnd := newEndIdx + 1
				if c.LineEnd != newLineEnd {
					c.LineEnd = newLineEnd
					c.AnchorContextEnd = GetContext(lines, newEndIdx)
					changed = true
				}
			}
			// Update replies
			for j := range comments {
				if comments[j].ParentID == c.ID {
					comments[j].Line = c.Line
					comments[j].LineEnd = c.LineEnd
				}
			}
		}
	}
	return changed
}

// ReanchorTourSteps re-anchors all steps in tours for a file. Returns true if any changed.
func ReanchorTourSteps(tours []protocol.Tour, file string, lines []string) bool {
	changed := false
	for ti := range tours {
		for si := range tours[ti].Steps {
			step := &tours[ti].Steps[si]
			if step.File != file || step.AnchorText == "" {
				continue
			}

			newIdx, orphaned, confidence := FindLine(lines, step.Line-1, step.AnchorText, step.AnchorContext)
			newLine := newIdx + 1

			newEndIdx := -1
			endOrphaned := false
			endConfidence := float32(1.0)
			if step.LineEnd > 0 && step.AnchorTextEnd != "" {
				newEndIdx, endOrphaned, endConfidence = FindLine(lines, step.LineEnd-1, step.AnchorTextEnd, step.AnchorContextEnd)
			}

			if (orphaned || endOrphaned) && !step.Orphaned {
				step.Orphaned = true
				step.Stale = false
				changed = true
			} else if !orphaned && !endOrphaned {
				if step.Orphaned {
					step.Orphaned = false
					changed = true
				}
				stale := confidence < staleThreshold || endConfidence < staleThreshold
				if step.Stale != stale {
					step.Stale = stale
					changed = true
				}
				if step.Line != newLine {
					step.Line = newLine
					step.AnchorContext = GetContext(lines, newIdx)
					changed = true
				}
				if newEndIdx >= 0 {
					newLineEnd := newEndIdx + 1
					if step.LineEnd != newLineEnd {
						step.LineEnd = newLineEnd
						step.AnchorContextEnd = GetContext(lines, newEndIdx)
						changed = true
					}
				}
			}
		}
	}
	return changed
}
