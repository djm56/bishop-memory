// Package model — Event is one row of the events table. The events
// table is append-only and backs /v1/events plus the durable audit trail.
//
// AppendEventRequest is the JSON body shape for POST /v1/events; the
// binding tags are the single source of truth for input validation
// (echoed in db/schema.sql CHECK constraints where applicable).
package model

// Event is one row of the events table. Fields map directly onto
// columns; JSON tags mirror the API wire format used by /v1/events.
type Event struct {
	// ID is the autoincrement primary key from the events table.
	ID int64 `json:"id"`

	// TaskID is the foreign key back to tasks.id, empty when the event
	// is not associated with a specific task (e.g., system / agent
	// events that do not belong to a single task).
	TaskID string `json:"task_id,omitempty"`

	// EventType is a dotted discriminator (task.created, task.updated,
	// task.run, agent.*, improvement.*).
	EventType string `json:"event_type"`

	// Summary is a short human-readable description of the event.
	Summary string `json:"summary"`

	// Agent is the actor attribution for the event (Phase 3). Populated
	// by the mcpd HTTP proxy as "<harness>:<sub-agent>" (e.g.
	// "opencode:orchestrator"); NULL for system / non-agent callers and
	// for the task-lifecycle events emitted by the task handlers.
	Agent string `json:"agent,omitempty"`

	// CreatedAt is the SQLite CURRENT_TIMESTAMP value at insert time
	// (UTC ISO-8601 string).
	CreatedAt string `json:"created_at"`
}

// AppendEventRequest is the JSON body for POST /v1/events.
//
// Validators mirror the column constraints in db/schema.sql (event_type
// and summary are NOT NULL; agent is the optional actor attribution
// added in Phase 3 to record who emitted the event) plus practical
// upper bounds to keep the audit log compact.
type AppendEventRequest struct {
	// TaskID is optional; empty means the event is system / agent-scoped
	// and does not link to a task row.
	TaskID string `json:"task_id,omitempty"`

	// EventType is required and bounded to 64 chars (the discriminator
	// is short by convention: "task.created", "agent.heartbeat", ...).
	EventType string `json:"event_type" binding:"required,max=64"`

	// Summary is required and bounded to 2000 chars.
	Summary string `json:"summary" binding:"required,max=2000"`

	// Agent is the actor attribution for the event. Optional so system /
	// non-agent callers can still post events without naming an agent.
	// The mcpd HTTP proxy composes Agent as "<harness>:<sub-agent>"
	// (e.g. "opencode:orchestrator") so the audit trail is traceable
	// back to the originating agent family.
	Agent string `json:"agent,omitempty" binding:"omitempty,max=64"`
}
