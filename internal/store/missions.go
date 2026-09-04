// Package store — data access for the missions table.
//
// Phase 2 will move the SQL currently inline in internal/api/missions.go into
// typed helpers here so the API layer only deals with models and contexts.
//
// TODO: implement in Phase 2 (see project_memory.md).
package store

// TODO: implement in Phase 2 — typed CRUD helpers for the missions table:
//   ListMissions(ctx, status string) ([]model.Mission, error)
//   GetMission(ctx, id string) (model.Mission, error)
//   CreateMission(ctx, mission model.Mission) error
//   UpdateMission(ctx, id string, patch model.UpdateMissionRequest) error
//   CreateMissionStep(ctx, missionID string, step model.MissionStep) error
