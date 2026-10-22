package model

// ClaimGradesRequest is the JSON body for POST /v1/mission-grades/claim.
// Without mission_ids the server claims the oldest finished missions that
// have no grade yet; with them it claims exactly those (each must be
// finished, and hold neither a verdict nor an exclusion).
type ClaimGradesRequest struct {
	RunID      *int64   `json:"run_id"`
	Limit      int64    `json:"limit" binding:"omitempty,min=1,max=10"`
	MissionIDs []string `json:"mission_ids" binding:"omitempty,max=10,dive,required,max=128"`
}

// GradeItem is one mission's verdict in a WriteGradesRequest. Exactly one of
// grade and insufficient is set: a letter, or "the database does not hold
// enough to grade this mission".
type GradeItem struct {
	MissionID    string `json:"mission_id" binding:"required,max=128"`
	Grade        string `json:"grade" binding:"omitempty,oneof=A B C D E F"`
	Insufficient bool   `json:"insufficient"`
	Summary      string `json:"summary" binding:"required,max=700"`
	Suggestions  string `json:"suggestions" binding:"omitempty,max=1000"`
}

// WriteGradesRequest is the JSON body for POST /v1/mission-grades.
type WriteGradesRequest struct {
	GradedBy string      `json:"graded_by" binding:"required,max=128"`
	RunID    *int64      `json:"run_id"`
	Items    []GradeItem `json:"items" binding:"required,min=1,max=10,dive"`
}

// ExclusionRequest is the JSON body for PUT /v1/mission-grades/:missionID/exclusion.
// Note says why the mission does not count. It is stored in the row's summary,
// which takes the same 700-character cap as a verdict's summary.
type ExclusionRequest struct {
	Note string `json:"note" binding:"required,max=700"`
}
