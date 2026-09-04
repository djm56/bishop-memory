// Package model — Directive is one row of the directives table (mirrors reference/DIRECTIVES.md).
//
// Directives are binding, human-ratified rules exposed read-only. No agent may write,
// update, or delete a directive. The API exposes no write path.
package model

// Directive is one row of the directives table. Fields map directly onto columns;
// JSON tags mirror the API wire format.
type Directive struct {
	ID          int64   `json:"id"`
	DirectiveID *string `json:"directive_id,omitempty"`
	Title       *string `json:"title,omitempty"`
	Rule        *string `json:"rule,omitempty"`
	Rationale   *string `json:"rationale,omitempty"`
	RatifiedAt  *string `json:"ratified_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
}
