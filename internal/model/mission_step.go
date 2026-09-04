package model

// MissionStep is one row of the mission_steps table returned by GET /v1/missions/:missionID/steps.
// All nullable columns use pointers so null can be distinguished from empty string.
type MissionStep struct {
	// ID is the autoincrement primary key from the mission_steps table.
	ID int64 `json:"id"`

	// MissionID is the foreign key back to missions.id.
	MissionID string `json:"mission_id"`

	// Step is the step label from PROGRESS.md (e.g., "1", "4a", "—").
	Step *string `json:"step,omitempty"`

	// Phase is the phase label from PROGRESS.md (e.g., "Core rename").
	Phase *string `json:"phase,omitempty"`

	// Agent is the agent name from PROGRESS.md (e.g., "@bishop", "@hicks").
	Agent *string `json:"agent,omitempty"`

	// Status is the step execution status (pending, in-progress, done, failed).
	Status *string `json:"status,omitempty"`

	// Notes is the notes field from PROGRESS.md.
	Notes *string `json:"notes,omitempty"`

	// StartedAt is the step start timestamp (free-form, no format validation).
	StartedAt *string `json:"started_at,omitempty"`

	// EndedAt is the step end timestamp (free-form, no format validation).
	EndedAt *string `json:"ended_at,omitempty"`

	// Summary is a summary of the step execution.
	Summary *string `json:"summary,omitempty"`

	// CreatedAt is the row creation timestamp (UTC ISO-8601).
	CreatedAt string `json:"created_at"`
}
