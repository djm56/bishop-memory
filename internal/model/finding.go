// Package model — Finding is one row of the findings table (mirrors findings/FINDINGS.md).
//
// Status transitions are human-driven only; agents never set or change a finding's status.
// The API exposes no write path for status, approver, or date_approved.
package model

// Finding is one row of the findings table. Fields map directly onto columns;
// JSON tags mirror the API wire format.
type Finding struct {
	ID           int64   `json:"id"`
	FindingDate  *string `json:"finding_date,omitempty"`
	Target       *string `json:"target,omitempty"`
	Suggestion   string  `json:"suggestion"`
	Rationale    *string `json:"rationale,omitempty"`
	Status       string  `json:"status"`
	Approver     *string `json:"approver,omitempty"`
	DateApproved *string `json:"date_approved,omitempty"`
	MissionID    *string `json:"mission_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

// CreateFindingRequest is the JSON body for POST /v1/findings.
//
// Status, approver, and date_approved are deliberately not accepted; they belong
// to the human operator alone. Every finding is created with status='proposed'.
// No agent may set these fields, and no route exists to update them.
type CreateFindingRequest struct {
	FindingDate string `json:"finding_date" binding:"omitempty,max=64"`
	Target      string `json:"target" binding:"omitempty,max=256"`
	Suggestion  string `json:"suggestion" binding:"required,max=2000"`
	Rationale   string `json:"rationale" binding:"omitempty,max=2000"`
	MissionID   string `json:"mission_id" binding:"omitempty,max=128"`
}
