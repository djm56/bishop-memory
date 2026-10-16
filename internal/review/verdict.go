// Package review reads a code review's outcome from the notes and summary a
// reviewer step records in mission_steps.
package review

import (
	"regexp"
	"strconv"
	"strings"
)

// Verdict values.
const (
	Approved    = "approved"
	NotApproved = "not-approved"
)

// Sources of a Verdict's values.
const (
	SourceLine = "line" // a fixed "Verdict:" line
	SourceText = "text" // free-text verdict wording
	SourceNone = "none" // no verdict wording found
)

// Verdict is the outcome of one review. Verdict is Approved, NotApproved, or
// "" when no verdict wording was found. Critical, Warnings and FixRound are
// nil when the text gives no count. ReviewsStep is "" when no reviewed step
// is named. Source says where the verdict came from.
type Verdict struct {
	Verdict     string
	Critical    *int
	Warnings    *int
	ReviewsStep string
	FixRound    *int
	Source      string
}

// verdictLine matches the fixed review line
// "Verdict: APPROVED|NOT APPROVED; critical=N; warnings=N; reviews_step=S",
// with an optional "; fix_round=N" at the end. It is case-sensitive, and
// NOT and APPROVED may be separated by spaces or a hyphen.
var verdictLine = regexp.MustCompile(`Verdict:\s*(NOT[\s-]+APPROVED|APPROVED)\s*;\s*critical=(\d+)\s*;\s*warnings=(\d+)\s*;\s*reviews_step=([0-9]+[A-Za-z0-9]*)(?:\s*;\s*fix_round=(\d+))?`)

// verdictWord matches verdict wording in free text, as a whole word. A
// negation ("not approved") matches in any case, with NOT and APPROVED
// separated by spaces or a hyphen, so no mix of cases reads "not approved" as
// approved. Other negations, such as "NOT APPROVE" or "NOT yet APPROVED", are
// not recognised and read as approved. The other words match in upper case
// only, and "Approved" also in title case; lower-case prose such as "approved
// by the operator" is not read as a verdict, but a title-case sentence such as
// "Approved by the operator" is.
var verdictWord = regexp.MustCompile(`\b((?i:not)[\s-]+(?i:approved)|CHANGES REQUESTED|CHANGES REQUIRED|NOT CLEAN|NO-GO|APPROVED|Approved|APPROVE)\b`)

// approvedWords are the verdictWord matches that mean approved; every other
// match means not approved.
var approvedWords = map[string]bool{"APPROVED": true, "Approved": true, "APPROVE": true}

// countWords are the counts read before "critical" and "warning(s)": a number
// word or digits; "no" and "zero" mean 0.
const countWords = `no|zero|one|two|three|four|five|six|seven|eight|nine|ten|\d+`

// criticalCount and warningCount capture an optional "step " prefix (group 1),
// so that a step number such as "step 23 critical path" is not read as a
// count, and the count (group 2).
var (
	criticalCount = regexp.MustCompile(`(?i)\b(step\s+)?(` + countWords + `)\s+critical\b`)
	warningCount  = regexp.MustCompile(`(?i)\b(step\s+)?(` + countWords + `)\s+warnings?\b`)
)

// reviewedStep matches "review step N", "reviewed step N" or "review of step
// N", or "review of N" when N is followed by punctuation or the end of the
// text, so that "review of 2 files" names no step.
var reviewedStep = regexp.MustCompile(`(?i)\breview(?:ed)?\s+(?:of\s+)?step\s+([0-9]+[a-z0-9]*)\b|\breview of ([0-9]+[a-z0-9]*)(?:[:.,;)]|\s*$)`)

// numberWords gives the value of each number word in countWords; the two lists
// must name the same words.
var numberWords = map[string]int{"no": 0, "zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10}

// Parse returns the outcome of a review from its step notes and summary.
//
// A fixed verdict line wins, searched in the notes and then in the summary.
// Otherwise the free text of the notes is read, or of the summary when the
// notes carry no verdict wording and the summary does; the verdict and the
// counts come from that text only, never merged across the two. Only the
// leftmost verdict wording is read, so a re-review that quotes an earlier
// verdict before its own reads as the earlier one. The reviewed step comes
// from that text, else the notes, else the summary. FixRound is set only from
// a verdict line.
func Parse(notes, summary string) Verdict {
	for _, text := range []string{notes, summary} {
		if m := verdictLine.FindStringSubmatch(text); m != nil {
			v := Verdict{Verdict: Approved, ReviewsStep: m[4], Source: SourceLine}
			if m[1] != "APPROVED" {
				v.Verdict = NotApproved
			}
			v.Critical = atoi(m[2])
			v.Warnings = atoi(m[3])
			if m[5] != "" {
				v.FixRound = atoi(m[5])
			}
			return v
		}
	}

	text := notes
	if !verdictWord.MatchString(notes) && verdictWord.MatchString(summary) {
		text = summary
	}
	v := Verdict{Source: SourceNone}
	if m := verdictWord.FindStringSubmatch(text); m != nil {
		v.Source = SourceText
		v.Verdict = NotApproved
		if approvedWords[m[1]] {
			v.Verdict = Approved
		}
	}
	v.Critical = count(criticalCount, text)
	v.Warnings = count(warningCount, text)
	for _, s := range []string{text, notes, summary} {
		if m := reviewedStep.FindStringSubmatch(s); m != nil {
			v.ReviewsStep = m[1] + m[2]
			break
		}
	}
	return v
}

// count returns the first count re captures in text that is not a step
// number, or nil when there is none.
func count(re *regexp.Regexp, text string) *int {
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			continue
		}
		if n, ok := numberWords[strings.ToLower(m[2])]; ok {
			return &n
		}
		return atoi(m[2])
	}
	return nil
}

// atoi returns s as an int, or nil when s is not a number.
func atoi(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}
