// Package model — Finding is one row of the findings table (mirrors findings/FINDINGS.md).
//
// Status transitions are human-driven only; agents never set or change a finding's status.
// The only write path for status, approver, date_approved and decision_note is the
// operator decision route (POST /v1/findings/:id/decision), which no MCP profile exposes.
package model

// Finding is one row of the findings table. Fields map directly onto columns;
// JSON tags mirror the API wire format.
//
// Triage and Recommendation are read-side joins (finding_triage and the one
// pending finding_recommendations row). They are nil when no classification or
// no pending recommendation exists.
type Finding struct {
	ID             int64                  `json:"id"`
	FindingDate    *string                `json:"finding_date,omitempty"`
	Target         *string                `json:"target,omitempty"`
	Suggestion     string                 `json:"suggestion"`
	Rationale      *string                `json:"rationale,omitempty"`
	Status         string                 `json:"status"`
	Approver       *string                `json:"approver,omitempty"`
	DateApproved   *string                `json:"date_approved,omitempty"`
	MissionID      *string                `json:"mission_id,omitempty"`
	Harness        *string                `json:"harness,omitempty"`
	DecisionNote   *string                `json:"decision_note,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	Triage         *FindingTriage         `json:"triage,omitempty"`
	Recommendation *FindingRecommendation `json:"recommendation,omitempty"`
}

// CreateFindingRequest is the JSON body for POST /v1/findings.
//
// Status, approver, and date_approved are deliberately not accepted; they belong
// to the human operator alone. If supplied in a request, they are silently dropped
// (standard JSON unmarshalling of unrecognised keys). Every finding is created with
// status='proposed'. Harness is the owning harness's identity (the value of
// BISHOP_HARNESS in that harness's conf); mcpd fills it from its environment and
// the reconciler from --harness.
type CreateFindingRequest struct {
	FindingDate string `json:"finding_date" binding:"omitempty,max=64"`
	Target      string `json:"target" binding:"omitempty,max=256"`
	Suggestion  string `json:"suggestion" binding:"required,max=2000"`
	Rationale   string `json:"rationale" binding:"omitempty,max=2000"`
	MissionID   string `json:"mission_id" binding:"omitempty,max=128"`
	Harness     string `json:"harness" binding:"omitempty,max=128"`
}

// FindingDecisionRequest is the JSON body for POST /v1/findings/:id/decision —
// the operator's status change. Status is the new ledger status; Approver is
// recorded on approve/applied and as the decider on every other transition;
// Note is required on reject, retired and superseded so the ledger carries the
// reason.
type FindingDecisionRequest struct {
	Status   string `json:"status" binding:"required,oneof=proposed approved applied rejected retired superseded"`
	Approver string `json:"approver" binding:"required,max=128"`
	Note     string `json:"note" binding:"omitempty,max=2000"`
	// DateApproved lets the reconciler carry the Markdown "Date approved"
	// value across verbatim. Omitted, the server uses today's UTC date.
	DateApproved string `json:"date_approved" binding:"omitempty,max=64"`
}
