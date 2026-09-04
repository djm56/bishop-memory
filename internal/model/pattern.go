// Package model — Pattern is one row of the patterns table (mirrors findings/PATTERNS.md).
//
// Patterns are advisory; they carry no status field and cannot be updated.
package model

// Pattern is one row of the patterns table. Fields map directly onto columns;
// JSON tags mirror the API wire format.
type Pattern struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	Context           *string `json:"context,omitempty"`
	Solution          *string `json:"solution,omitempty"`
	Example           *string `json:"example,omitempty"`
	DiscoveredAt      *string `json:"discovered_at,omitempty"`
	DiscoveredMission *string `json:"discovered_mission,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

// CreatePatternRequest is the JSON body for POST /v1/patterns.
type CreatePatternRequest struct {
	Name              string `json:"name" binding:"required,max=256"`
	Context           string `json:"context" binding:"omitempty,max=2000"`
	Solution          string `json:"solution" binding:"omitempty,max=2000"`
	Example           string `json:"example" binding:"omitempty,max=2000"`
	DiscoveredAt      string `json:"discovered_at" binding:"omitempty,max=64"`
	DiscoveredMission string `json:"discovered_mission" binding:"omitempty,max=128"`
}
