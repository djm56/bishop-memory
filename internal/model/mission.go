package model

type Mission struct {
	// ID is the unique mission identifier (PRIMARY KEY).
	ID string `json:"id"`

	// Title is the human-readable mission title.
	Title string `json:"title"`

	// Status is the working state (not-started, in-progress, blocked, complete).
	Status string `json:"status"`

	// Outcome is the terminal state (done, failed), independently settable from
	// status. Outcome may remain NULL while status is in-progress; the harness
	// sets status to "complete" at one step and outcome at a later one, and the
	// API mirrors that behaviour by not requiring outcome when status becomes
	// "complete".
	Outcome *string `json:"outcome,omitempty"`

	// Owner is the mission owner name (human-readable identifier).
	Owner *string `json:"owner,omitempty"`

	// Priority is the mission priority (low, normal, high, urgent).
	Priority string `json:"priority"`

	// NextAction is the free-form next-action note.
	NextAction *string `json:"next_action,omitempty"`

	// Blockers is the free-form blockers note.
	Blockers *string `json:"blockers,omitempty"`

	// OpenedAt is the row creation timestamp (UTC ISO-8601).
	OpenedAt string `json:"opened_at"`

	// ClosedAt is the row completion timestamp (set when status becomes "complete").
	ClosedAt *string `json:"closed_at,omitempty"`

	// CreatedAt is the row creation timestamp (UTC ISO-8601).
	CreatedAt string `json:"created_at"`

	// UpdatedAt is the row last-update timestamp (UTC ISO-8601).
	UpdatedAt string `json:"updated_at"`
}

type CreateMissionRequest struct {
	ID         string `json:"id" binding:"required,max=128"`
	Title      string `json:"title" binding:"required,max=500"`
	Status     string `json:"status" binding:"omitempty,oneof=not-started in-progress blocked complete"`
	Owner      string `json:"owner" binding:"omitempty,max=128"`
	Priority   string `json:"priority" binding:"omitempty,oneof=low normal high urgent"`
	NextAction string `json:"next_action" binding:"omitempty,max=2000"`
	Blockers   string `json:"blockers" binding:"omitempty,max=2000"`
}

type UpdateMissionRequest struct {
	Status     *string `json:"status" binding:"omitempty,oneof=not-started in-progress blocked complete"`
	Owner      *string `json:"owner" binding:"omitempty,max=128"`
	Outcome    *string `json:"outcome" binding:"omitempty,oneof=done failed"`
	Priority   *string `json:"priority" binding:"omitempty,oneof=low normal high urgent"`
	NextAction *string `json:"next_action" binding:"omitempty,max=2000"`
	Blockers   *string `json:"blockers" binding:"omitempty,max=2000"`
}
