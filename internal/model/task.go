package model

type Task struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Priority   string  `json:"priority"`
	NextAction *string `json:"next_action,omitempty"`
	Blockers   *string `json:"blockers,omitempty"`
	OpenedAt   string  `json:"opened_at"`
	ClosedAt   *string `json:"closed_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

type CreateTaskRequest struct {
	ID         string `json:"id" binding:"required,max=128"`
	Title      string `json:"title" binding:"required,max=500"`
	Status     string `json:"status" binding:"omitempty,oneof=open active blocked complete cancelled"`
	Priority   string `json:"priority" binding:"omitempty,oneof=low normal high urgent"`
	NextAction string `json:"next_action" binding:"omitempty,max=2000"`
	Blockers   string `json:"blockers" binding:"omitempty,max=2000"`
}

type UpdateTaskRequest struct {
	Status     *string `json:"status" binding:"omitempty,oneof=open active blocked complete cancelled"`
	Priority   *string `json:"priority" binding:"omitempty,oneof=low normal high urgent"`
	NextAction *string `json:"next_action" binding:"omitempty,max=2000"`
	Blockers   *string `json:"blockers" binding:"omitempty,max=2000"`
}
