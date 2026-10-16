package debrief

import (
	"regexp"
	"strings"
)

// Lesson kinds produced by the parsers in this file, as stored in
// mission_lessons.kind.
const (
	LessonWrongAssumption = "wrong-assumption"
	LessonSimilarMission  = "similar-mission"
	LessonReuseStep       = "reuse-step"
)

// Lesson is one mission_lessons row. Text holds the assumption, the reuse
// trigger or the checklist step. Why and Correction are set only on
// wrong-assumption rows and are "" otherwise.
type Lesson struct {
	Kind       string
	Text       string
	Why        string
	Correction string
}

// triggerLine matches a "Trigger…:" line in a "Similar Future Missions"
// section, with an optional "-" or "*" bullet marker and optional "**" bold
// markers around the label, and captures the text after the colon.
var triggerLine = regexp.MustCompile(`(?i)^\s*(?:[-*]\s+)?(?:\*\*)?trigger[^:*]*(?:\*\*)?:(?:\*\*)?\s*(.*?)\s*$`)

// stepLine matches a numbered line ("1. text"), indented or not, and captures
// the text after the number.
var stepLine = regexp.MustCompile(`^\s*\d+\.\s+(.*\S)\s*$`)

// ParseWrongAssumptions reads the table in a debrief's "Wrong Assumptions"
// section body (the heading excluded). The header row is the first row
// containing '|' that is not a separator row and whose first cell is
// "Assumption" (case-insensitive). Each following row, up to the first blank
// line or line without '|', gives one Lesson of kind wrong-assumption from
// exactly three cells: Text, Why, Correction, each trimmed. Separator rows are
// passed over. A row whose first cell is a None word, or is empty or only
// dashes, is not returned and not counted. A row with any other cell count is
// not returned and is counted in skipped. ok is false when there is no header
// row. Carriage returns are removed from the section before it is read.
func ParseWrongAssumptions(section string) (rows []Lesson, skipped int, ok bool) {
	section = strings.ReplaceAll(section, "\r", "")
	lines := strings.Split(section, "\n")
	header := -1
	for i, line := range lines {
		if !looksLikeTableRow(line) || isSeparatorRow(line) {
			continue
		}
		cells := splitRow(line)
		if len(cells) > 0 && strings.EqualFold(strings.TrimSpace(cells[0]), "assumption") {
			header = i
			break
		}
	}
	if header < 0 {
		return nil, 0, false
	}
	for _, line := range lines[header+1:] {
		if strings.TrimSpace(line) == "" || !looksLikeTableRow(line) {
			break
		}
		if isSeparatorRow(line) {
			continue
		}
		cells := splitRow(line)
		if len(cells) > 0 {
			first := strings.TrimSpace(cells[0])
			if isNoneRow(first) || isCellAllDashes(first) {
				continue
			}
		}
		if len(cells) != 3 {
			skipped++
			continue
		}
		rows = append(rows, Lesson{
			Kind:       LessonWrongAssumption,
			Text:       strings.TrimSpace(cells[0]),
			Why:        strings.TrimSpace(cells[1]),
			Correction: strings.TrimSpace(cells[2]),
		})
	}
	return rows, skipped, true
}

// ParseSimilarMission reads a debrief's "Similar Future Missions" section
// body (the heading excluded). Every numbered line ("1. text", indented or
// not) gives one Lesson of kind reuse-step with the text after the number, in
// order. Of the other lines, the first that matches triggerLine with
// non-empty text after its colon gives one Lesson of kind similar-mission.
// The trigger, when found, comes first in rows, then the steps. Other lines
// are ignored. ok is false when the section gives no trigger and no step.
func ParseSimilarMission(section string) (rows []Lesson, ok bool) {
	var trigger *Lesson
	var steps []Lesson
	for _, line := range strings.Split(section, "\n") {
		if m := stepLine.FindStringSubmatch(line); m != nil {
			steps = append(steps, Lesson{Kind: LessonReuseStep, Text: m[1]})
			continue
		}
		if trigger == nil {
			if m := triggerLine.FindStringSubmatch(line); m != nil && m[1] != "" {
				trigger = &Lesson{Kind: LessonSimilarMission, Text: m[1]}
			}
		}
	}
	if trigger != nil {
		rows = append(rows, *trigger)
	}
	rows = append(rows, steps...)
	return rows, len(rows) > 0
}
