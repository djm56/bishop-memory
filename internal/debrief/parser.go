// Package debrief parses sections from mission debriefs.
//
// This package provides parsing logic for extracting structured data from
// DEBRIEF.md sections, such as "Sub-Agent Mistakes and Corrections" tables.
package debrief

import (
	"strings"
)

// Mistake represents one row from a "Sub-Agent Mistakes and Corrections" table.
// AgentRaw is the raw text from the Agent cell (e.g., "@hicks").
// Impact is the severity level; empty string represents NULL (an empty, "none" or
// dash-only cell, or a cell whose first word is not a level).
// ImpactNote is the text that goes with the Impact cell: the words after the level
// (or after "none"), or the whole cell when its first word is not a level.
type Mistake struct {
	AgentRaw   string // Raw text from the Agent cell (e.g., "@hicks")
	Mistake    string // Description of what went wrong
	Impact     string // "low", "med", "high", or "" (empty for NULL)
	ImpactNote string // Text after the level or "none", or the whole cell when its first word is not a level
	Correction string // Corrective action taken
	Prevention string // Prevention strategy for next time
}

// ParseMistakes extracts mistake rows from the "Sub-Agent Mistakes and Corrections"
// section body (the table content, excluding the heading). Returns the parsed rows,
// the count of skipped rows (wrong cell count only), and whether the table was readable.
// ok is false only when no table with an "Agent" header is found.
//
// Parsing rules:
//   - Finds the first table whose header row has "Agent" (case-insensitive, trimmed)
//     in the first cell.
//   - Skips separator rows: lines made only of '|', '-', ':' and spaces. That includes
//     a pipe-only line, which is skipped without being counted.
//   - The table ends at the first blank line or the first line with no '|'. Every line
//     before that which contains a '|' is treated as a row, so prose that happens to
//     contain a '|' is a row with the wrong cell count: it is skipped and counted, and
//     reading continues with the line after it.
//   - None rows store nothing and are not counted as skipped, regardless of cell count.
//     A row is a None row if (a) its Agent cell is a None word, OR (b) its Mistake cell
//     starts with a None word AND its Impact cell is empty, "none" (nothing else,
//     case-insensitive), "—", "–" or only hyphens.
//     "None word" = trimmed text equals "none" case-insensitively, or starts with "none"
//     (case-insensitive) followed by a space or punctuation (word boundary).
//   - Rows must have exactly 5 data cells after splitting on unescaped '|' and dropping
//     the single leading and single trailing empty cell produced by edge pipes.
//   - Rows with the wrong cell count are skipped and counted.
//   - Cells with "\|" have the backslash removed, converting to "|".
//   - The Impact cell is read by parseImpact.
//   - Empty table (header and separator only) returns ok=true with 0 rows.
func ParseMistakes(section string) (rows []Mistake, skipped int, ok bool) {
	if section == "" {
		return nil, 0, false
	}

	lines := strings.Split(section, "\n")

	var headerIdx int
	found := false

	// Find the header row (first row that is not a separator).
	for i, line := range lines {
		if isSeparatorRow(line) {
			continue
		}
		// Check if this row starts with "Agent".
		cells := splitRow(line)
		if len(cells) > 0 && strings.EqualFold(strings.TrimSpace(cells[0]), "agent") {
			headerIdx = i
			found = true
			break
		}
	}

	if !found {
		return nil, 0, false
	}

	ok = true

	// Process rows after the header.
	for i := headerIdx + 1; i < len(lines); i++ {
		line := lines[i]

		// End of table on a blank line or a line with no pipe.
		if strings.TrimSpace(line) == "" || !looksLikeTableRow(line) {
			break
		}

		// Skip separator rows.
		if isSeparatorRow(line) {
			continue
		}

		cells := splitRow(line)

		// Check for "None" rows before cell count check; they are not counted as skipped.
		// A row is a None row if:
		//   (a) its Agent cell (first cell) is a None word, OR
		//   (b) its Mistake cell (second cell) starts with a None word AND
		//       its Impact cell (third) is empty, "none", "—", "–" or only hyphens.
		if isNoneRowByRules(cells) {
			continue
		}

		// Need exactly 5 cells.
		if len(cells) != 5 {
			skipped++
			continue
		}

		agentCell := strings.TrimSpace(cells[0])
		mistakeCell := strings.TrimSpace(cells[1])
		impactCell := strings.TrimSpace(cells[2])
		correctionCell := strings.TrimSpace(cells[3])
		preventionCell := strings.TrimSpace(cells[4])

		impact, impactNote := parseImpact(impactCell)

		rows = append(rows, Mistake{
			AgentRaw:   agentCell,
			Mistake:    mistakeCell,
			Impact:     impact,
			ImpactNote: impactNote,
			Correction: correctionCell,
			Prevention: preventionCell,
		})
	}

	return rows, skipped, ok
}

// isSeparatorRow returns true if the line is non-blank and contains only '|', '-', ':'
// and space characters (a pipe-only line qualifies).
func isSeparatorRow(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	for _, ch := range line {
		if ch != '|' && ch != '-' && ch != ':' && ch != ' ' {
			return false
		}
	}
	return true
}

// looksLikeTableRow returns true if the line contains a pipe character (not just prose).
func looksLikeTableRow(line string) bool {
	return strings.Contains(line, "|")
}

// splitRow splits a table row on unescaped '|' characters.
// It drops only the single leading empty cell (produced by a leading |)
// and the single trailing empty cell (produced by a trailing |), keeping all interior cells.
func splitRow(line string) []string {
	var cells []string
	var current strings.Builder
	var i int
	for i < len(line) {
		ch := line[i]
		if ch == '\\' && i+1 < len(line) && line[i+1] == '|' {
			// Escaped pipe: unescape and add to current cell.
			current.WriteByte('|')
			i += 2
		} else if ch == '|' {
			// Unescaped pipe: end current cell.
			cells = append(cells, current.String())
			current.Reset()
			i++
		} else {
			current.WriteByte(ch)
			i++
		}
	}
	// Append the final cell.
	cells = append(cells, current.String())

	// Trim all cells.
	trimmed := make([]string, 0, len(cells))
	for _, cell := range cells {
		trimmed = append(trimmed, strings.TrimSpace(cell))
	}

	// Drop only the single leading empty cell (from leading |).
	start := 0
	if len(trimmed) > 0 && trimmed[0] == "" {
		start = 1
	}

	// Drop only the single trailing empty cell (from trailing |).
	end := len(trimmed)
	if end > start && trimmed[end-1] == "" {
		end--
	}

	return trimmed[start:end]
}

// parseImpact reads the Impact cell and returns (impact, note).
//
//  1. The cell is trimmed. If it is empty, or made only of hyphens, '—', '–' and
//     spaces, the result is ("", "").
//  2. The cell is split once at its first '—', '–' or spaced " - " into head and
//     tail (both trimmed). With none of those, head is the whole cell and tail is "".
//     A hyphen with no spaces around it, as in "low-risk", is not a separator.
//  3. The first word of head, lower-cased with trailing ',', '+', ':', ';' and '.'
//     stripped, decides the result. "none" gives impact "". "low" gives "low",
//     "med" or "medium" gives "med", "high" gives "high". For "none" and for the
//     three levels, note = rest of head + " " + tail, trimmed. Any other word gives
//     ("", the whole trimmed cell).
//
// A bare "none" is rule 3 with an empty rest and tail, so its note is "".
func parseImpact(cell string) (impact, note string) {
	cell = strings.TrimSpace(cell)

	if isCellAllDashes(cell) {
		return "", ""
	}

	head, tail := cell, ""
	if idx := findDashSeparatorIndex(cell); idx != -1 {
		runes := []rune(cell)
		head = strings.TrimSpace(string(runes[:idx]))
		tail = strings.TrimSpace(string(runes[idx+1:]))
	}

	word := strings.ToLower(extractFirstWord(head))
	rest := joinNote(extractRestAfterFirstWord(head), tail)

	if word == "none" {
		return "", rest
	}
	if level := normalizeImpact(word); level != "" {
		return level, rest
	}
	return "", cell
}

// joinNote joins the two note fragments with one space and trims the result, so an
// empty fragment leaves no stray space.
func joinNote(restOfHead, tail string) string {
	return strings.TrimSpace(restOfHead + " " + tail)
}

// isCellAllDashes returns true if the cell is empty or contains only '-', '—', '–'
// and spaces.
func isCellAllDashes(cell string) bool {
	for _, ch := range cell {
		if ch != '-' && ch != '—' && ch != '–' && ch != ' ' {
			return false
		}
	}
	return true
}

// findDashSeparatorIndex returns the rune index of the first recognized dash separator
// ('—', '–', or the '-' of a spaced " - "), or -1 if there is none.
func findDashSeparatorIndex(s string) int {
	runes := []rune(s)
	for i, ch := range runes {
		if ch == '—' || ch == '–' {
			return i
		}
		// Spaced hyphen " - ": position i is the space before the hyphen.
		if i+2 < len(runes) && ch == ' ' && runes[i+1] == '-' && runes[i+2] == ' ' {
			return i + 1
		}
	}
	return -1
}

// extractFirstWord returns the first whitespace-delimited word of text with trailing
// ',', '+', ':', ';' and '.' characters removed, or "" if text has no word.
func extractFirstWord(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRightFunc(fields[0], func(r rune) bool {
		return r == ',' || r == '+' || r == ':' || r == '.' || r == ';'
	})
}

// extractRestAfterFirstWord returns the words after the first whitespace-delimited
// word of text, joined by single spaces, or "" if there is at most one word.
func extractRestAfterFirstWord(text string) string {
	fields := strings.Fields(text)
	if len(fields) <= 1 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

// normalizeImpact maps a lower-case impact word to "low", "med" or "high"
// ("medium" maps to "med"). It returns "" for any other word and does not lower-case
// its input.
func normalizeImpact(word string) string {
	switch word {
	case "low":
		return "low"
	case "med", "medium":
		return "med"
	case "high":
		return "high"
	default:
		return ""
	}
}

// isNoneRow returns true if s equals "none" (case-insensitive) or starts with "none"
// followed by a space or punctuation (word boundary).
func isNoneRow(s string) bool {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if lower == "none" {
		return true
	}
	if !strings.HasPrefix(lower, "none") {
		return false
	}
	// "none" found; check character after it (word boundary).
	if len(s) > 4 {
		ch := s[4]
		return ch == ' ' || isPunctuation(ch)
	}
	return false
}

// isPunctuation returns true if ch is punctuation.
func isPunctuation(ch byte) bool {
	return ch == ',' || ch == '.' || ch == ';' || ch == ':' || ch == '!' || ch == '?'
}

// isNoneRowByRules returns true if the row is a None row according to:
//
//	(a) Agent cell (first cell) is a None word, OR
//	(b) Mistake cell (second cell) starts with a None word AND the Impact cell
//	    (third) satisfies isNoneImpactCell.
//
// Rule (b) needs at least 3 cells; with fewer, only rule (a) applies.
func isNoneRowByRules(cells []string) bool {
	if len(cells) == 0 {
		return false
	}

	// Rule (a): Agent cell (first cell) is a None word.
	agent := strings.TrimSpace(cells[0])
	if agent != "" && isNoneRow(agent) {
		return true
	}

	// Rule (b): Mistake cell (second) starts with a None word AND the Impact cell
	// (third) is a None Impact cell.
	if len(cells) >= 3 {
		mistake := strings.TrimSpace(cells[1])
		impact := strings.TrimSpace(cells[2])

		if mistake != "" && isNoneRow(mistake) && isNoneImpactCell(impact) {
			return true
		}
	}

	return false
}

// isNoneImpactCell returns true if the trimmed Impact cell is empty, "none" (any case,
// and nothing else), "—", "–", or made only of '-' characters. It is deliberately
// narrower than isNoneRow: "none — see note" is not a None Impact cell.
func isNoneImpactCell(impact string) bool {
	impact = strings.TrimSpace(impact)
	switch {
	case impact == "", impact == "—", impact == "–":
		return true
	case strings.EqualFold(impact, "none"):
		return true
	}
	return impact != "" && strings.Trim(impact, "-") == ""
}
