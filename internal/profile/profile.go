// Package profile holds the values that make the service Bishop-shaped: the
// memory-tree layout the importer reads, the reviewer role the crew views
// depend on, and the harness identity used when none is set.
//
// Bishop is the only profile, and Bishop is the only value in use. The logic
// that reads these values does not name them itself, so the names live in one
// place. No configuration selects another profile yet; that choice belongs to
// a later phase of docs/plans/HARNESS-PORTABILITY-PLAN.md.
package profile

// Layout names the directories and files of a harness memory tree that the
// importer reads. Each name is a single path element. ServiceRecordsDir is
// relative to FindingsDir, and AgentsDir sits beside the memory root rather
// than inside it.
type Layout struct {
	MissionsDir       string
	FindingsDir       string
	ReferenceDir      string
	ServiceRecordsDir string
	AgentsDir         string
	CurrentMission    string
	FlightRecorder    string
	Brief             string
	Progress          string
	Debrief           string
	Patterns          string
	Directives        string
}

// Profile is the set of Bishop-specific values the service reads.
type Profile struct {
	// Layout is the memory tree the importer and the crew views read.
	Layout Layout
	// ReviewerRole is the crew name of the reviewer, compared against the
	// normalised agent name by CrewName.
	ReviewerRole string
	// DefaultHarness is the harness identity used when BISHOP_HARNESS is unset
	// or empty. It is the Bishop harness's own identity. The project's MCP
	// registration sets BISHOP_HARNESS, so only a bare invocation reaches this
	// default.
	DefaultHarness string
}

// Bishop is the Bishop harness's profile. It is the only Profile in use.
var Bishop = Profile{
	Layout: Layout{
		MissionsDir:       "missions",
		FindingsDir:       "findings",
		ReferenceDir:      "reference",
		ServiceRecordsDir: "service-records",
		AgentsDir:         "agents",
		CurrentMission:    "CURRENT-MISSION.md",
		FlightRecorder:    "FLIGHT-RECORDER.md",
		Brief:             "BRIEF.md",
		Progress:          "PROGRESS.md",
		Debrief:           "DEBRIEF.md",
		Patterns:          "PATTERNS.md",
		Directives:        "DIRECTIVES.md",
	},
	ReviewerRole:   "apone",
	DefaultHarness: "claude-code",
}
