// Package model — FlightRecorderEntry is one row of the flight_recorder table. The flight_recorder
// table is append-only and backs /v1/flight-recorder plus the durable audit trail.
//
// AppendFlightRecorderRequest is the JSON body shape for POST /v1/flight-recorder; the
// binding tags are the single source of truth for input validation
// (echoed in db/schema.sql CHECK constraints where applicable).
package model

// FlightRecorderEntry is one row of the flight_recorder table. Fields map directly onto
// columns; JSON tags mirror the API wire format used by /v1/flight-recorder.
type FlightRecorderEntry struct {
	// ID is the autoincrement primary key from the flight_recorder table.
	ID int64 `json:"id"`

	// MissionID is the foreign key back to missions.id, empty when the event
	// is not associated with a specific mission (e.g., system / agent
	// events that do not belong to a single mission).
	MissionID string `json:"mission_id,omitempty"`

	// Step is the harness step label (e.g., "4a", "—"), present on journal rows
	// (step-sync, complete, blocked) and null on handler-emitted audit rows.
	Step *string `json:"step,omitempty"`

	// OccurredAt is the caller-supplied event timestamp from the harness
	// (authoritative timestamp), separate from CreatedAt which is the row
	// insert time. Null on handler-emitted audit rows.
	OccurredAt *string `json:"occurred_at,omitempty"`

	// Event is a dotted discriminator (mission.created, mission.updated,
	// mission.step, agent.*, improvement.*).
	Event string `json:"event"`

	// Note is a short human-readable description of the event.
	Note string `json:"note"`

	// Agent is the actor attribution for the event (Phase 3). Populated
	// by the mcpd HTTP proxy as "<harness>:<sub-agent>" (e.g.
	// "anomalous:hicks"); NULL for system / non-agent callers and
	// for the mission-lifecycle events emitted by the mission handlers.
	Agent string `json:"agent,omitempty"`

	// CreatedAt is the SQLite CURRENT_TIMESTAMP value at insert time
	// (UTC ISO-8601 string).
	CreatedAt string `json:"created_at"`
}

// AppendFlightRecorderRequest is the JSON body for POST /v1/flight-recorder.
//
// Validators mirror the column constraints in db/schema.sql (event
// and note are NOT NULL; agent is the optional actor attribution
// added in Phase 3 to record who emitted the event) plus practical
// upper bounds to keep the audit log compact.
type AppendFlightRecorderRequest struct {
	// MissionID is optional; empty means the event is system / agent-scoped
	// and does not link to a mission row.
	MissionID string `json:"mission_id,omitempty"`

	// Step is the harness step label (e.g., "4a", "—"). Optional; null
	// on handler-emitted audit rows.
	Step string `json:"step" binding:"omitempty,max=16"`

	// Event is required and bounded to 64 chars (the discriminator
	// is short by convention: "mission.created", "agent.heartbeat", ...).
	Event string `json:"event" binding:"required,max=64"`

	// Note is required and bounded to 2000 chars.
	Note string `json:"note" binding:"required,max=2000"`

	// OccurredAt is the caller-supplied event timestamp (expected format:
	// YYYY-MM-DD HH:MM UTC). No format validation is applied; this field
	// is free-form to match the unvalidated timestamp fields started_at
	// and ended_at already in the schema. Null on handler-emitted audit rows.
	OccurredAt string `json:"occurred_at" binding:"omitempty,max=64"`

	// Agent is the actor attribution for the event. Optional so system /
	// non-agent callers can still post events without naming an agent.
	// The mcpd HTTP proxy composes Agent as "<harness>:<sub-agent>"
	// (e.g. "anomalous:hicks") so the audit trail is traceable
	// back to the originating agent family.
	Agent string `json:"agent,omitempty" binding:"omitempty,max=64"`
}
