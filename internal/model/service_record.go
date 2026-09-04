// Package model — ServiceRecord is one row of the service_records table.
//
// Records observations about agent performance and behaviour, tracking both
// self-reported (from agent IMPROVEMENT-NOTE) and bishop-observed entries.
package model

// ServiceRecord is one row of the service_records table. Fields map directly onto columns;
// JSON tags mirror the API wire format.
type ServiceRecord struct {
	ID         int64   `json:"id"`
	Agent      string  `json:"agent"`
	RecordDate *string `json:"record_date,omitempty"`
	Title      *string `json:"title,omitempty"`
	Note       *string `json:"note,omitempty"`
	Adjustment *string `json:"adjustment,omitempty"`
	Source     *string `json:"source,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// CreateServiceRecordRequest is the JSON body for POST /v1/service-records.
//
// Agent is required (NOT NULL in the schema). Source must validate to
// 'self-reported' or 'bishop-observed' if present; a bad value returns 400.
type CreateServiceRecordRequest struct {
	Agent      string `json:"agent" binding:"required,max=128"`
	RecordDate string `json:"record_date" binding:"omitempty,max=64"`
	Title      string `json:"title" binding:"omitempty,max=256"`
	Note       string `json:"note" binding:"omitempty,max=2000"`
	Adjustment string `json:"adjustment" binding:"omitempty,max=2000"`
	Source     string `json:"source" binding:"omitempty,oneof=self-reported bishop-observed"`
}
