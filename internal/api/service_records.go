// Package api — HTTP handlers for /v1/service-records.
//
// Service records track observations about agent performance and behaviour.
// Source must validate to 'self-reported' or 'bishop-observed' if present.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/model"
)

// serviceRecordSources is the valid source values for service records.
// Matches the CHECK constraint in db/schema.sql.
var serviceRecordSources = []string{"self-reported", "bishop-observed"}

// listServiceRecordsHandler handles GET /v1/service-records?agent=<optional>.
// Returns service records newest-first (ORDER BY id DESC).
// The optional agent filter returns only records for that agent.
func listServiceRecordsHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		agent := strings.TrimSpace(c.Query("agent"))

		query := `
			SELECT id, agent, record_date, title, note, adjustment, source, created_at
			FROM service_records
		`
		args := []any{}

		if agent != "" {
			query += ` WHERE agent = ?`
			args = append(args, agent)
		}

		query += ` ORDER BY id DESC`

		rows, err := db.QueryContext(c.Request.Context(), query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()

		records := make([]model.ServiceRecord, 0)

		for rows.Next() {
			record, err := scanServiceRecord(rows)
			if err != nil {
				internalError(c, err)
				return
			}
			records = append(records, record)
		}

		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"service_records": records})
	}
}

// createServiceRecordHandler handles POST /v1/service-records.
// Creates a new service record. Agent is required; source is optional but validated.
// Audit row appended to flight_recorder in the same transaction.
func createServiceRecordHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request model.CreateServiceRecordRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		// Trim free-form string fields for consistency.
		request.Agent = strings.TrimSpace(request.Agent)
		request.RecordDate = strings.TrimSpace(request.RecordDate)
		request.Title = strings.TrimSpace(request.Title)
		request.Note = strings.TrimSpace(request.Note)
		request.Adjustment = strings.TrimSpace(request.Adjustment)
		request.Source = strings.TrimSpace(request.Source)

		// Validate source if present against the schema CHECK constraint.
		if request.Source != "" && !isOneOf(request.Source, serviceRecordSources) {
			validationError(c, errors.New("source must be one of: self-reported, bishop-observed"))
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO service_records (
				agent, record_date, title, note, adjustment, source
			) VALUES (?, ?, ?, ?, ?, ?)`,
			request.Agent,
			nullIfEmpty(request.RecordDate),
			nullIfEmpty(request.Title),
			nullIfEmpty(request.Note),
			nullIfEmpty(request.Adjustment),
			nullIfEmpty(request.Source),
		)
		if err != nil {
			internalError(c, err)
			return
		}

		id, err := result.LastInsertId()
		if err != nil {
			internalError(c, err)
			return
		}

		// Audit row: leave mission_id, step, occurred_at NULL.
		_, err = tx.ExecContext(
			c.Request.Context(),
			`INSERT INTO flight_recorder (
				event, note
			) VALUES (?, ?)`,
			"service_record.created",
			"Service record created",
		)
		if err != nil {
			internalError(c, err)
			return
		}

		if err := tx.Commit(); err != nil {
			internalError(c, err)
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":      id,
			"created": true,
		})
	}
}

func scanServiceRecord(row scanner) (model.ServiceRecord, error) {
	var record model.ServiceRecord
	var recordDate sql.NullString
	var title sql.NullString
	var note sql.NullString
	var adjustment sql.NullString
	var source sql.NullString

	err := row.Scan(
		&record.ID,
		&record.Agent,
		&recordDate,
		&title,
		&note,
		&adjustment,
		&source,
		&record.CreatedAt,
	)

	if recordDate.Valid {
		record.RecordDate = &recordDate.String
	}
	if title.Valid {
		record.Title = &title.String
	}
	if note.Valid {
		record.Note = &note.String
	}
	if adjustment.Valid {
		record.Adjustment = &adjustment.String
	}
	if source.Valid {
		record.Source = &source.String
	}

	return record, err
}
