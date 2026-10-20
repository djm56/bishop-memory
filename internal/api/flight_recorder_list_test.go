package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
)

// secondOf gives row i (1-based) a distinct created_at, one second apart, so
// the newest-first order is the insert order.
func secondOf(i int) string {
	return fmt.Sprintf("2026-10-20 10:%02d:%02d", i/60%60, i%60)
}

// sameSecond gives every row the same created_at, so the id tie-break decides the order.
func sameSecond(int) string {
	return "2026-10-20 10:00:00"
}

// seedJournal inserts n flight-recorder rows for mission m-1, row i carrying
// createdAt(i). Ids run 1..n because the table is empty before the call.
func seedJournal(t *testing.T, db *sql.DB, n int, createdAt func(int) string) {
	t.Helper()
	insertMission(t, db, "m-1", "kirsch", "in-progress")
	for i := 1; i <= n; i++ {
		if _, err := db.Exec(
			`INSERT INTO flight_recorder (mission_id, event, note, created_at) VALUES ('m-1', 'mission.step', ?, ?)`,
			fmt.Sprintf("row %d", i), createdAt(i),
		); err != nil {
			t.Fatalf("seed journal row %d: %v", i, err)
		}
	}
}

// journalIDs returns the ids of a list response's flight_recorder array, in order.
func journalIDs(t *testing.T, resp map[string]any) []int {
	t.Helper()
	raw, ok := resp["flight_recorder"].([]any)
	if !ok {
		t.Fatalf("response has no flight_recorder array: %v", resp)
	}
	ids := make([]int, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("flight_recorder element is %T, want object", item)
		}
		id, ok := row["id"].(float64)
		if !ok {
			t.Fatalf("flight_recorder element has no numeric id: %v", row)
		}
		ids = append(ids, int(id))
	}
	return ids
}

// rangeIDs returns the ids from hi down to lo inclusive, the newest-first order of rows lo..hi.
func rangeIDs(hi, lo int) []int {
	ids := []int{}
	for id := hi; id >= lo; id-- {
		ids = append(ids, id)
	}
	return ids
}

// hasMoreOf returns the response's has_more value and whether the key is present.
func hasMoreOf(t *testing.T, resp map[string]any) (bool, bool) {
	t.Helper()
	raw, present := resp["has_more"]
	if !present {
		return false, false
	}
	value, ok := raw.(bool)
	if !ok {
		t.Fatalf("has_more is %T, want bool", raw)
	}
	return value, true
}

// equalIDs reports whether two id lists match element for element.
func equalIDs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestListFlightRecorderPaging drives GET /v1/flight-recorder through the
// router (NewRouter), so the query string is parsed by the same route the
// reconciler calls. Each subtest is one edge of the offset contract and seeds
// its own database.
func TestListFlightRecorderPaging(t *testing.T) {
	t.Run("absent offset keeps the unpaged response", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 120, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1", nil)
		if code != http.StatusOK {
			t.Fatalf("absent offset: status %d, want 200: %v", code, resp)
		}
		if _, present := hasMoreOf(t, resp); present {
			t.Errorf("absent offset: response carries has_more; the unpaged response must not")
		}
		if got, want := journalIDs(t, resp), rangeIDs(120, 71); !equalIDs(got, want) {
			t.Errorf("absent offset: default page is %v, want the newest 50 rows %v", got, want)
		}
	})

	t.Run("first page reports rows after it", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 120, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&limit=100&offset=0", nil)
		if code != http.StatusOK {
			t.Fatalf("first page: status %d, want 200: %v", code, resp)
		}
		if got, want := journalIDs(t, resp), rangeIDs(120, 21); !equalIDs(got, want) {
			t.Errorf("first page: got %d rows %v, want the newest 100 rows %v", len(got), got, want)
		}
		if more, present := hasMoreOf(t, resp); !present || !more {
			t.Errorf("first page: has_more = %v (present %v), want true with 20 rows after it", more, present)
		}
	})

	t.Run("last page reports nothing after it", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 120, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&limit=100&offset=100", nil)
		if code != http.StatusOK {
			t.Fatalf("last page: status %d, want 200: %v", code, resp)
		}
		if got, want := journalIDs(t, resp), rangeIDs(20, 1); !equalIDs(got, want) {
			t.Errorf("last page: got %v, want the oldest 20 rows %v", got, want)
		}
		if more, present := hasMoreOf(t, resp); !present || more {
			t.Errorf("last page: has_more = %v (present %v), want false", more, present)
		}
	})

	t.Run("exact page with nothing after it reports no more", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 100, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&limit=100&offset=0", nil)
		if code != http.StatusOK {
			t.Fatalf("exact page: status %d, want 200: %v", code, resp)
		}
		if got, want := journalIDs(t, resp), rangeIDs(100, 1); !equalIDs(got, want) {
			t.Errorf("exact page: got %d rows, want all 100 rows", len(got))
		}
		if more, present := hasMoreOf(t, resp); !present || more {
			t.Errorf("exact page: has_more = %v (present %v), want false with no row after the page", more, present)
		}
	})

	t.Run("offset past the last row is an empty page", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 120, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&limit=100&offset=500", nil)
		if code != http.StatusOK {
			t.Fatalf("beyond end: status %d, want 200: %v", code, resp)
		}
		if got := journalIDs(t, resp); len(got) != 0 {
			t.Errorf("beyond end: got rows %v, want an empty array", got)
		}
		if more, present := hasMoreOf(t, resp); !present || more {
			t.Errorf("beyond end: has_more = %v (present %v), want false", more, present)
		}
	})

	t.Run("non-integer offset is rejected", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 3, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&offset=abc", nil)
		if code != http.StatusBadRequest {
			t.Errorf("non-integer offset: status %d, want 400: %v", code, resp)
		}
	})

	t.Run("negative offset is rejected", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 3, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&offset=-1", nil)
		if code != http.StatusBadRequest {
			t.Errorf("negative offset: status %d, want 400: %v", code, resp)
		}
	})

	t.Run("empty offset is rejected", func(t *testing.T) {
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 3, secondOf)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&offset=", nil)
		if code != http.StatusBadRequest {
			t.Errorf("empty offset: status %d, want 400: %v", code, resp)
		}
	})

	t.Run("rows sharing created_at page in id order", func(t *testing.T) {
		// All five rows share one created_at, so only the id tie-break orders them.
		// Paging by two must return 5,4 then 3,2 then 1 with no repeat and no gap.
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 5, sameSecond)
		var all []int
		for _, offset := range []int{0, 2, 4} {
			path := fmt.Sprintf("/v1/flight-recorder?mission_id=m-1&limit=2&offset=%d", offset)
			code, resp := do(t, router, http.MethodGet, path, nil)
			if code != http.StatusOK {
				t.Fatalf("tie page offset %d: status %d, want 200: %v", offset, code, resp)
			}
			all = append(all, journalIDs(t, resp)...)
		}
		if want := rangeIDs(5, 1); !equalIDs(all, want) {
			t.Errorf("tie order paged by two: ids %v, want id-descending %v", all, want)
		}
	})

	t.Run("rows sharing created_at come back in id order without offset", func(t *testing.T) {
		// The same five rows read in one unpaged request: the id tie-break orders the
		// default page as well.
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 5, sameSecond)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1", nil)
		if code != http.StatusOK {
			t.Fatalf("tie unpaged: status %d, want 200: %v", code, resp)
		}
		if got, want := journalIDs(t, resp), rangeIDs(5, 1); !equalIDs(got, want) {
			t.Errorf("tie order without offset: ids %v, want id-descending %v", got, want)
		}
	})

	t.Run("created_at outranks id in the order", func(t *testing.T) {
		// The opposite edge of the tie: here created_at differs and falls as the id
		// rises, so row 1 is the newest. The id tie-break must not override created_at,
		// so the order is 1,2,3,4,5 and not id-descending. Read paged and unpaged.
		router, db := newTriageTestRouter(t)
		seedJournal(t, db, 5, reverseSecond)
		code, resp := do(t, router, http.MethodGet, "/v1/flight-recorder?mission_id=m-1&limit=5&offset=0", nil)
		if code != http.StatusOK {
			t.Fatalf("created_at precedence: status %d, want 200: %v", code, resp)
		}
		if got, want := journalIDs(t, resp), []int{1, 2, 3, 4, 5}; !equalIDs(got, want) {
			t.Errorf("created_at precedence: ids %v, want newest-created_at first %v", got, want)
		}
	})
}

// reverseSecond gives row i (1-based) a created_at that falls as i rises, so row 1
// is the newest and the newest-first order is ascending in id.
func reverseSecond(i int) string {
	return fmt.Sprintf("2026-10-20 10:00:%02d", 59-i)
}
