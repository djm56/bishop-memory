// Package store — data access for the flight_recorder table.
//
// FlightRecorderEntry is append-only records of notable activity (mission.created,
// mission.updated, mission.step, agent.*, improvement.*, ...). They back the
// /v1/flight-recorder endpoints and provide a durable audit trail.
//
// TODO: implement in Phase 2 (see docs/migration-plan.md).
package store

// TODO: implement in Phase 2 — typed helpers for the flight_recorder table:
//   AppendFlightRecorderEntry(ctx, entry model.FlightRecorderEntry) error
//   ListFlightRecorderEntries(ctx, missionID string, limit int) ([]model.FlightRecorderEntry, error)
