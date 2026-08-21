package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestIsFilesystemRoot is a table-driven unit test for the minimal
// refuse-filesystem-root guard added in Step 4 (the judgement item —
// README.md's "Root `'/'` edge case" deferred item, scoped down from a
// full allow-list to just this rejection).
func TestIsFilesystemRoot(t *testing.T) {
	cases := []struct {
		name string
		root string
		want bool
	}{
		{name: "literal slash", root: "/", want: true},
		{name: "slash with trailing separator", root: "//", want: true},
		{name: "ordinary absolute path", root: "/var/lib/bishop-memory", want: false},
		{name: "ordinary relative path", root: "testdata/memory", want: false},
		{name: "single dot (cwd, not root)", root: ".", want: false},
		{name: "empty string (cwd, not root)", root: "", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isFilesystemRoot(tc.root)
			if got != tc.want {
				t.Errorf("isFilesystemRoot(%q) = %v, want %v", tc.root, got, tc.want)
			}
		})
	}
}

// TestSyncDocumentsHandler_RejectsFilesystemRoot is the Step 4 brief's
// required observation for the root-safety judgement item: exercise
// the actual HTTP handler (not just the isFilesystemRoot helper in
// isolation) and confirm a request naming "/" as root is rejected
// with 400 before importer.Sync ever runs. The handler is given a nil
// *sql.DB — this is only safe BECAUSE the guard is checked before any
// database use; if the guard were removed or reordered, this test
// would panic on a nil-pointer dereference inside importer.Sync
// instead of just failing the status-code assertion, so a regression
// here is loud, not silently green.
func TestSyncDocumentsHandler_RejectsFilesystemRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/v1/documents/sync", syncDocumentsHandler(nil))

	body, err := json.Marshal(map[string]string{"root": "/"})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/documents/sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if decoded["error"] != "invalid request" {
		t.Fatalf(`decoded["error"] = %v, want "invalid request"`, decoded["error"])
	}
}
