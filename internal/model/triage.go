// Package model — the findings-triage objects: categories, runs, per-finding
// classification, groups, recommendations and directive proposals.
//
// These are written by the triage agents (through mcpd's triage profile) and by
// the operator (through the decision routes). None of them is a write path to
// findings.status; see finding.go.
package model

// FindingCategory is one row of finding_categories.
type FindingCategory struct {
	Slug            string  `json:"slug"`
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Examples        *string `json:"examples,omitempty"`
	SortOrder       int64   `json:"sort_order"`
	Active          bool    `json:"active"`
	LastProcessedAt *string `json:"last_processed_at,omitempty"`
	CreatedAt       string  `json:"created_at"`
	// Counts are filled by the list route so the review page and the
	// processor's rotation can see the backlog per category.
	ProposedCount int64 `json:"proposed_count"`
	PendingCount  int64 `json:"pending_count"`
}

// UpsertFindingCategoryRequest is the JSON body for PUT /v1/finding-categories/:slug.
type UpsertFindingCategoryRequest struct {
	Name        string `json:"name" binding:"required,max=128"`
	Description string `json:"description" binding:"required,max=2000"`
	Examples    string `json:"examples" binding:"omitempty,max=2000"`
	SortOrder   int64  `json:"sort_order"`
	Active      *bool  `json:"active"`
}

// TriageRun is one row of triage_runs.
type TriageRun struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Category   *string `json:"category,omitempty"`
	Model      *string `json:"model,omitempty"`
	Status     string  `json:"status"`
	Considered int64   `json:"considered"`
	Written    int64   `json:"written"`
	Notes      *string `json:"notes,omitempty"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at,omitempty"`
	// Accepted and Declined count the operator's decisions on the
	// recommendations this run produced. Filled by the list route.
	Accepted int64 `json:"accepted"`
	Declined int64 `json:"declined"`
}

// StartTriageRunRequest is the JSON body for POST /v1/triage/runs.
type StartTriageRunRequest struct {
	Kind     string `json:"kind" binding:"required,oneof=classify process grade"`
	Category string `json:"category" binding:"omitempty,max=64"`
	Model    string `json:"model" binding:"omitempty,max=128"`
}

// FinishTriageRunRequest is the JSON body for PATCH /v1/triage/runs/:id.
type FinishTriageRunRequest struct {
	Status     string `json:"status" binding:"required,oneof=done failed"`
	Considered *int64 `json:"considered"`
	Written    *int64 `json:"written"`
	Notes      string `json:"notes" binding:"omitempty,max=4000"`
}

// FindingTriage is one row of finding_triage.
type FindingTriage struct {
	FindingID          int64    `json:"finding_id"`
	Category           string   `json:"category"`
	SecondaryCategory  *string  `json:"secondary_category,omitempty"`
	DirectiveCandidate bool     `json:"directive_candidate"`
	Confidence         *float64 `json:"confidence,omitempty"`
	Summary            *string  `json:"summary,omitempty"`
	ClassifiedBy       string   `json:"classified_by"`
	RunID              *int64   `json:"run_id,omitempty"`
	ClassifiedAt       string   `json:"classified_at"`
}

// ClassificationItem is one element of PUT /v1/triage/classifications.
type ClassificationItem struct {
	FindingID          int64    `json:"finding_id" binding:"required"`
	Category           string   `json:"category" binding:"required,max=64"`
	SecondaryCategory  string   `json:"secondary_category" binding:"omitempty,max=64"`
	DirectiveCandidate bool     `json:"directive_candidate"`
	Confidence         *float64 `json:"confidence" binding:"omitempty,min=0,max=1"`
	Summary            string   `json:"summary" binding:"omitempty,max=200"`
}

// ClassifyRequest is the JSON body for PUT /v1/triage/classifications.
type ClassifyRequest struct {
	ClassifiedBy string               `json:"classified_by" binding:"required,max=128"`
	RunID        *int64               `json:"run_id"`
	Items        []ClassificationItem `json:"items" binding:"required,min=1,max=200,dive"`
}

// FindingGroup is one row of finding_groups.
type FindingGroup struct {
	ID        int64   `json:"id"`
	Category  string  `json:"category"`
	Title     string  `json:"title"`
	Summary   string  `json:"summary"`
	Target    *string `json:"target,omitempty"`
	RunID     *int64  `json:"run_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// CreateFindingGroupRequest is the JSON body for POST /v1/finding-groups.
type CreateFindingGroupRequest struct {
	Category string `json:"category" binding:"required,max=64"`
	Title    string `json:"title" binding:"required,max=200"`
	Summary  string `json:"summary" binding:"required,max=2000"`
	Target   string `json:"target" binding:"omitempty,max=256"`
	RunID    *int64 `json:"run_id"`
}

// FindingRecommendation is one row of finding_recommendations.
type FindingRecommendation struct {
	ID             int64   `json:"id"`
	FindingID      int64   `json:"finding_id"`
	GroupID        *int64  `json:"group_id,omitempty"`
	Recommendation string  `json:"recommendation"`
	SupersededBy   *int64  `json:"superseded_by,omitempty"`
	Rationale      string  `json:"rationale"`
	ProposedChange *string `json:"proposed_change,omitempty"`
	State          string  `json:"state"`
	DecidedBy      *string `json:"decided_by,omitempty"`
	DecidedAt      *string `json:"decided_at,omitempty"`
	RunID          *int64  `json:"run_id,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

// RecommendationItem is one element of POST /v1/finding-recommendations.
type RecommendationItem struct {
	FindingID      int64  `json:"finding_id" binding:"required"`
	GroupID        *int64 `json:"group_id"`
	Recommendation string `json:"recommendation" binding:"required,oneof=approve reject supersede defer"`
	SupersededBy   *int64 `json:"superseded_by"`
	Rationale      string `json:"rationale" binding:"required,max=2000"`
	ProposedChange string `json:"proposed_change" binding:"omitempty,max=8000"`
}

// RecommendRequest is the JSON body for POST /v1/finding-recommendations.
type RecommendRequest struct {
	RunID *int64               `json:"run_id"`
	Items []RecommendationItem `json:"items" binding:"required,min=1,max=200,dive"`
}

// DirectiveProposal is one row of directive_proposals.
type DirectiveProposal struct {
	ID            int64   `json:"id"`
	GroupID       *int64  `json:"group_id,omitempty"`
	Harness       *string `json:"harness,omitempty"`
	Title         string  `json:"title"`
	AppliesWhen   string  `json:"applies_when"`
	Rule          string  `json:"rule"`
	Rationale     string  `json:"rationale"`
	ReviewerCheck string  `json:"reviewer_check"`
	Example       *string `json:"example,omitempty"`
	Evidence      []int64 `json:"evidence"`
	State         string  `json:"state"`
	DirectiveID   *string `json:"directive_id,omitempty"`
	DecidedBy     *string `json:"decided_by,omitempty"`
	DecidedAt     *string `json:"decided_at,omitempty"`
	DecisionNote  *string `json:"decision_note,omitempty"`
	RunID         *int64  `json:"run_id,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// CreateDirectiveProposalRequest is the JSON body for POST /v1/directive-proposals.
// The six template fields are capped so the rendered entry can fit the
// DIRECTIVES-TEMPLATE length budget (1,510 characters absolute maximum).
type CreateDirectiveProposalRequest struct {
	GroupID       *int64  `json:"group_id"`
	Harness       string  `json:"harness" binding:"omitempty,max=128"`
	Title         string  `json:"title" binding:"required,max=60"`
	AppliesWhen   string  `json:"applies_when" binding:"required,max=300"`
	Rule          string  `json:"rule" binding:"required,max=800"`
	Rationale     string  `json:"rationale" binding:"required,max=300"`
	ReviewerCheck string  `json:"reviewer_check" binding:"required,max=400"`
	Example       string  `json:"example" binding:"omitempty,max=300"`
	Evidence      []int64 `json:"evidence" binding:"required,min=1"`
	RunID         *int64  `json:"run_id"`
}

// DirectiveProposalDecisionRequest is the JSON body for
// POST /v1/directive-proposals/:id/decision. On accept the operator may
// edit the six fields in place before ratifying; omitted fields keep the
// proposal's text.
type DirectiveProposalDecisionRequest struct {
	State         string  `json:"state" binding:"required,oneof=accepted declined"`
	DecidedBy     string  `json:"decided_by" binding:"required,max=128"`
	Note          string  `json:"note" binding:"omitempty,max=2000"`
	Title         *string `json:"title" binding:"omitempty,max=60"`
	AppliesWhen   *string `json:"applies_when" binding:"omitempty,max=300"`
	Rule          *string `json:"rule" binding:"omitempty,max=800"`
	Rationale     *string `json:"rationale" binding:"omitempty,max=300"`
	ReviewerCheck *string `json:"reviewer_check" binding:"omitempty,max=400"`
	Example       *string `json:"example" binding:"omitempty,max=300"`
}

// Harness is one row of harnesses.
type Harness struct {
	Name       string `json:"name"`
	MemoryRoot string `json:"memory_root"`
	LastSeenAt string `json:"last_seen_at"`
}

// UpsertHarnessRequest is the JSON body for PUT /v1/harnesses/:name.
type UpsertHarnessRequest struct {
	MemoryRoot string `json:"memory_root" binding:"required,max=1024"`
}

// UpsertDirectiveRequest is the JSON body for PUT /v1/directives/:directiveID,
// used by the reconciler to mirror DIRECTIVES.md. Not registered in any mcpd
// profile.
type UpsertDirectiveRequest struct {
	Title      string `json:"title" binding:"required,max=256"`
	Rule       string `json:"rule" binding:"required,max=4000"`
	Rationale  string `json:"rationale" binding:"omitempty,max=2000"`
	RatifiedAt string `json:"ratified_at" binding:"omitempty,max=64"`
}
