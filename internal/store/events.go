// Package store — data access for the events table.
//
// Events are append-only records of notable activity (task.created,
// task.updated, task.run, agent.*, improvement.*, ...). They back the
// /v1/events endpoints and provide a durable audit trail.
//
// TODO: implement in Phase 2 (see project_memory.md).
package store

// TODO: implement in Phase 2 — typed helpers for the events table:
//   AppendEvent(ctx, ev model.Event) error
//   ListEvents(ctx, taskID string, limit int) ([]model.Event, error)
