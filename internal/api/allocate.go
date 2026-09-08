package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// allocateMissionRequest is the body of POST /v1/missions/allocate.
//
// There is deliberately no `id` field: the whole point of this endpoint is
// that the SERVICE picks the id. A caller that already knows the id it
// wants should use POST /v1/missions instead.
type allocateMissionRequest struct {
	// Harness identifies the caller — the value of BISHOP_HARNESS in the
	// calling harness's configuration. It is recorded on the mission so a
	// single bishop-memory serving several harnesses can attribute every
	// mission to the one that opened it. Required: an unattributed
	// centrally-allocated mission defeats the purpose of allocating
	// centrally.
	Harness string `json:"harness" binding:"required,max=128"`

	Title      string `json:"title" binding:"required,max=500"`
	Owner      string `json:"owner" binding:"omitempty,max=128"`
	Priority   string `json:"priority" binding:"omitempty,oneof=low normal high urgent"`
	NextAction string `json:"next_action" binding:"omitempty,max=2000"`
	Blockers   string `json:"blockers" binding:"omitempty,max=2000"`

	// Date overrides the UTC day the id is scoped to, as YYYYMMDD. Present
	// for testing and for backdating a mission that is being registered
	// late; omitted in normal operation, where the server's own UTC clock
	// is the only sensible source. Callers cannot be trusted to agree on
	// "today" across timezones, which is exactly why the default is
	// server-side.
	Date string `json:"date" binding:"omitempty,len=8,numeric"`
}

// missionIDPattern matches the ids this endpoint allocates and that the
// harness's mission-lifecycle doctrine defines: mission-YYYYMMDD-NN, where
// NN is at least two digits and grows to three or more past 99.
//
// The trailing anchor matters. Without it this would also match a
// harness-qualified id such as mission-20260907-anom-01, whose "01" is not
// a global sequence number, and folding those into the max would hand out
// a colliding id.
var missionIDPattern = regexp.MustCompile(`^mission-(\d{8})-(\d+)$`)

// allocateRetryLimit bounds the compute-then-insert retry loop. Each
// iteration is a full transaction, so a genuine race resolves in one or
// two rounds; a limit this high is only ever reached if something is
// pathologically wrong, and returning 503 there is more honest than
// looping forever.
const allocateRetryLimit = 10

// allocateMissionHandler handles POST /v1/missions/allocate.
//
// It exists because mission ids must be unique across MULTIPLE harnesses.
// A harness deriving its own id — scanning its own mission folders and
// journal for the highest NN taken today — is correct in isolation and
// wrong the moment a second harness does the same thing on the same UTC
// day: both compute 01, both write mission-20260907-01, and the audit
// trail becomes ambiguous. That ambiguity is the one thing the journal
// exists to prevent.
//
// ALLOCATION IS CREATION. The endpoint does not hand out an id and trust
// the caller to use it. Reserving an id in one call and inserting the row
// in another leaves a window in which a second caller computes the same
// next sequence, and no amount of care on the client side closes it. By
// inserting the mission inside the same transaction that computes the
// sequence, the row itself IS the reservation, and the PRIMARY KEY on
// missions.id is what makes that guarantee real rather than advisory.
//
// CONCURRENCY. Two callers can still read the same maximum before either
// writes. Rather than depend on SQLite lock escalation or a particular
// transaction mode, the handler simply lets the PRIMARY KEY reject the
// loser and retries: recompute the max, try the next id, up to
// allocateRetryLimit times. This is correct regardless of isolation level
// and needs no reservation table.
func allocateMissionHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request allocateMissionRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			validationError(c, err)
			return
		}

		request.Harness = strings.TrimSpace(request.Harness)
		request.Title = strings.TrimSpace(request.Title)
		if request.Harness == "" || request.Title == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "harness and title must be non-empty",
			})
			return
		}
		if request.Priority == "" {
			request.Priority = "normal"
		}

		date := strings.TrimSpace(request.Date)
		if date == "" {
			// UTC, matching the harness's own clock for FLIGHT-RECORDER.md
			// and MISSION-ARCHIVE.md timestamps, so the day in an id and
			// the day in the journal never disagree.
			date = time.Now().UTC().Format("20060102")
		}

		for attempt := 0; attempt < allocateRetryLimit; attempt++ {
			id, seq, err := tryAllocate(c, db, date, request)
			if err == nil {
				c.JSON(http.StatusCreated, gin.H{
					"id":       id,
					"harness":  request.Harness,
					"date":     date,
					"seq":      seq,
					"created":  true,
					"attempts": attempt + 1,
				})
				return
			}
			if errors.Is(err, errIDTaken) {
				// Lost the race. Recompute against the now-larger maximum.
				continue
			}
			internalError(c, err)
			return
		}

		// Every attempt collided. Something is generating ids far faster
		// than this loop can settle, or a non-sequence id is colliding.
		// 503 with Retry-After is the honest answer: the request is valid
		// and may well succeed shortly.
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": fmt.Sprintf(
				"could not allocate a mission id for %s after %d attempts", date, allocateRetryLimit),
		})
	}
}

// errIDTaken signals that the chosen id lost a race and the caller should
// recompute. It is never returned to the client.
var errIDTaken = errors.New("mission id already taken")

// tryAllocate runs one compute-then-insert attempt in a single
// transaction. It returns the allocated id and its sequence number, or
// errIDTaken if the id was claimed concurrently.
func tryAllocate(c *gin.Context, db *sql.DB, date string, request allocateMissionRequest) (string, int, error) {
	ctx := c.Request.Context()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()

	next, err := nextSequence(ctx, tx, date)
	if err != nil {
		return "", 0, err
	}

	// %02d pads to two digits and widens naturally past 99, which is what
	// the harness doctrine specifies for a day that runs long.
	id := fmt.Sprintf("mission-%s-%02d", date, next)

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO missions (
			id, title, status, owner, priority, next_action, blockers, harness
		) VALUES (?, ?, 'not-started', ?, ?, ?, ?, ?)`,
		id,
		request.Title,
		nullIfEmpty(request.Owner),
		request.Priority,
		nullIfEmpty(request.NextAction),
		nullIfEmpty(request.Blockers),
		request.Harness,
	)
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) &&
			(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
				sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return "", 0, errIDTaken
		}
		return "", 0, err
	}

	// Mirror createMissionHandler: the mission and its audit row commit
	// together or not at all.
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO flight_recorder (
			mission_id, agent, event, note
		) VALUES (?, ?, 'mission.allocated', ?)`,
		id,
		request.Harness,
		fmt.Sprintf("Mission id allocated to harness %q: %s", request.Harness, request.Title),
	)
	if err != nil {
		return "", 0, err
	}

	if err := tx.Commit(); err != nil {
		// A commit can still fail on a uniqueness conflict if the driver
		// defers the check; treat that as a lost race too rather than a
		// server fault.
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) &&
			(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
				sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
			return "", 0, errIDTaken
		}
		return "", 0, err
	}

	return id, next, nil
}

// nextSequence returns the next free NN for a UTC day.
//
// The maximum is taken over EVERY mission for that day regardless of which
// harness opened it — that global view is the entire reason allocation is
// centralised. Ids that do not match mission-YYYYMMDD-NN exactly are
// ignored rather than guessed at, so a hand-written or legacy id shaped
// differently cannot corrupt the sequence.
func nextSequence(ctx context.Context, tx *sql.Tx, date string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM missions WHERE id LIKE ?`, "mission-"+date+"-%")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	highest := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		match := missionIDPattern.FindStringSubmatch(id)
		if match == nil || match[1] != date {
			continue
		}
		n, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	return highest + 1, nil
}
