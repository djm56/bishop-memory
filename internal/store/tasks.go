// Package store — data access for the tasks table.
//
// Phase 2 will move the SQL currently inline in internal/api/tasks.go into
// typed helpers here so the API layer only deals with models and contexts.
//
// TODO: implement in Phase 2 (see project_memory.md).
package store

// TODO: implement in Phase 2 — typed CRUD helpers for the tasks table:
//   ListTasks(ctx, status string) ([]model.Task, error)
//   GetTask(ctx, id string) (model.Task, error)
//   CreateTask(ctx, task model.Task) error
//   UpdateTask(ctx, id string, patch model.UpdateTaskRequest) error
//   CreateTaskRun(ctx, taskID string, run model.TaskRun) error
