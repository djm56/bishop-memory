// Package debrief parses sections from mission debriefs.
//
// This package provides parsing logic for extracting structured data from
// DEBRIEF.md sections, such as "Sub-Agent Mistakes and Corrections" tables.
package debrief

import (
	"strings"
	"unicode"
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

// Deliverable represents one bullet from a "Deliverables Changed" section.
// Path is the file path extracted from backticks or bold formatting, or empty string for NULL.
// Note is the description text.
type Deliverable struct {
	Path string // File path or empty string for NULL
	Note string // Description text
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

// ParseDeliverables extracts deliverable rows from the "Deliverables Changed" section body
// (without the heading). Returns the parsed rows, the count of skipped sections (always 0),
// and whether the section was readable.
// ok is false only when the section contains no top-level bullets at all.
//
// Parsing rules:
//   - Top-level bullets: lines whose first character is '-' or '*' followed by a space,
//     with no leading indentation. Lines indented by space or tab, numbered lines,
//     table lines (|), headings (#), rules (---), blank lines and prose are not rows.
//   - None rows: a bullet whose text, trimmed and with one trailing '.' removed, is empty,
//     case-insensitively equals "none", or consists only of '-', '*', '—', '–' and spaces,
//     is a none-row: it is not returned and is not counted.
//   - Path from backticks: if text starts with ` (backtick), extracts the backtick-delimited span,
//     trims it, and checks if it is a valid path. If valid, path is the span; otherwise path is ""
//     and note is the whole bullet text with backticks kept. Rest of text follows the closing backtick.
//   - Path from bold: if text starts with **, extracts **X** and reads X in this order: strip one
//     trailing ':', strip one pair of surrounding backticks, strip one more trailing ':'. A result
//     that still contains a backtick is not a path. Otherwise the result is checked as a valid path.
//     If valid, path is the result. If not, path is "" and note is whole text.
//   - No path: otherwise path is "" and note is whole text.
//   - Separator: when a path was found, strip exactly one leading separator from the rest
//     (in order checked): " — ", " – ", " - ", " : ", ": ". Otherwise, when the whole rest,
//     trimmed, equals a single "—", "–" or "-", the note is "" (this includes a dash with no space
//     before it). Otherwise a leading ":" is stripped. Then trim; what remains is the note.
//   - No top-level bullets: returns (nil, 0, false).
func ParseDeliverables(section string) (rows []Deliverable, skipped int, ok bool) {
	lines := strings.Split(section, "\n")

	var topLevelBullets []string
	for _, line := range lines {
		if isTopLevelBullet(line) {
			topLevelBullets = append(topLevelBullets, line)
		}
	}

	// No top-level bullets at all.
	if len(topLevelBullets) == 0 {
		return nil, 0, false
	}

	// Process each top-level bullet.
	for _, bullet := range topLevelBullets {
		text := extractBulletText(bullet)
		text = strings.TrimSpace(text)

		// Check if it's a none-row.
		if isNoneBullet(text) {
			continue
		}

		// Extract path and the rest of the text.
		path, rest := extractPath(text)

		// Handle separator between path and note.
		note := rest
		if path != "" {
			note = stripSeparator(rest)
		}

		rows = append(rows, Deliverable{Path: path, Note: note})
	}

	return rows, 0, true
}

// isTopLevelBullet returns true if the line is a top-level bullet (starts with - or *
// followed by space, with no leading indentation).
func isTopLevelBullet(line string) bool {
	if line == "" {
		return false
	}
	// Check for leading whitespace.
	if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return false
	}
	// Must start with - or * followed by space.
	if len(line) < 2 {
		return false
	}
	if (line[0] == '-' || line[0] == '*') && line[1] == ' ' {
		return true
	}
	return false
}

// extractBulletText returns the text of a bullet after removing the leading marker (- or *).
func extractBulletText(bullet string) string {
	if len(bullet) < 2 {
		return ""
	}
	// Skip the leading - or * and the space.
	return bullet[2:]
}

// isNoneBullet returns true if the trimmed text, with trailing '.' removed, is empty,
// case-insensitively equals "none", or consists only of '-', '*', '—', '–' and space characters.
func isNoneBullet(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true // Empty bullet is a none-row
	}
	text = strings.TrimSuffix(text, ".")
	text = strings.TrimSpace(text)
	if text == "" {
		return true // Empty after trimming suffix
	}
	lower := strings.ToLower(text)
	if lower == "none" {
		return true
	}
	// Check if text consists only of '-', '*', '—', '–' and spaces.
	for _, ch := range text {
		if ch != '-' && ch != '*' && ch != '—' && ch != '–' && ch != ' ' {
			return false
		}
	}
	return true // Contains only hyphens, em dashes, en dashes, asterisks and spaces
}

// extractPath returns the path and the rest of the text.
// If path starts with backtick, extracts the backtick-delimited span, trims it, and applies
// isValidPath. If valid, returns (span, rest); if not or if span is empty, returns ("", text)
// with backticks kept in the note.
// If path starts with **, extracts **X** and reads X in this order: strips one trailing ':',
// strips one pair of surrounding backticks, strips one more trailing ':'. A result that
// still contains a backtick is not a path; otherwise isValidPath is applied. If valid,
// returns (the result, rest after the closing **). Otherwise returns ("", text).
func extractPath(text string) (path, rest string) {
	// Try backticks first: `X`
	if strings.HasPrefix(text, "`") {
		if idx := strings.Index(text[1:], "`"); idx != -1 {
			path = strings.TrimSpace(text[1 : idx+1])
			rest = text[idx+2:]
			// Backtick span: check if it is a valid path.
			if path != "" && isValidPath(path) {
				return path, rest
			}
			// Empty or invalid: return empty path and whole text as note.
			return "", text
		}
	}

	// Try bold: **X**
	if strings.HasPrefix(text, "**") {
		endIdx := strings.Index(text[2:], "**")
		if endIdx != -1 {
			boldContent := text[2 : endIdx+2]
			// Strip one trailing ':' (outside any backticks).
			stripped := strings.TrimSuffix(boldContent, ":")
			// Strip one pair of surrounding backticks.
			stripped = stripSurroundingBackticks(stripped)
			// Strip one trailing ':' from the result.
			stripped = strings.TrimSuffix(stripped, ":")
			// If the result still contains a backtick, it is not a path.
			if strings.Contains(stripped, "`") {
				return "", text
			}
			// Check if it's a valid path.
			if isValidPath(stripped) {
				rest = text[endIdx+4:]
				return stripped, rest
			}
		}
		// Bold didn't match; return whole text as note.
		return "", text
	}

	// No path found.
	return "", text
}

// stripSurroundingBackticks strips one pair of surrounding backticks if present.
func stripSurroundingBackticks(s string) string {
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		return s[1 : len(s)-1]
	}
	return s
}

// isValidPath returns true if the path contains no whitespace and contains "/" or ".".
func isValidPath(path string) bool {
	if path == "" {
		return false
	}
	// Check for whitespace.
	for _, ch := range path {
		if unicode.IsSpace(ch) {
			return false
		}
	}
	// Must contain "/" or ".".
	return strings.ContainsAny(path, "/.")
}

// stripSeparator strips exactly one leading separator from the text and returns the result.
// Separators checked in order: " — ", " – ", " - ", " : ", ": ". Then, when the whole text,
// trimmed, equals a single "—", "–" or "-", the result is "". Then a leading ":" is stripped.
// Returns the trimmed result.
func stripSeparator(text string) string {
	// Try the multi-character separators first (with leading space).
	for _, sep := range []string{" — ", " – ", " - ", " : ", ": "} {
		if strings.HasPrefix(text, sep) {
			return strings.TrimSpace(strings.TrimPrefix(text, sep))
		}
	}

	// Lone dash: only when the whole rest, trimmed, equals that dash alone.
	rest := strings.TrimSpace(text)
	if rest == "—" {
		return ""
	}
	if rest == "–" {
		return ""
	}
	if rest == "-" {
		return ""
	}

	// Bare ":" with no space after it.
	if strings.HasPrefix(text, ":") {
		return strings.TrimSpace(strings.TrimPrefix(text, ":"))
	}

	return strings.TrimSpace(text)
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
