package api

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
)

// harnessRoot is one memory root to sync and the harness it belongs to.
// Name is empty for a root no harness is registered on.
type harnessRoot struct {
	Name string
	Root string
}

// registeredHarnessRoots returns every registered harness memory root, once
// per root. When two harness names share a root, the most recently seen name
// is used.
func registeredHarnessRoots(ctx context.Context, db *sql.DB) ([]harnessRoot, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, memory_root FROM harnesses ORDER BY last_seen_at DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]bool{}
	var roots []harnessRoot
	for rows.Next() {
		var h harnessRoot
		if err := rows.Scan(&h.Name, &h.Root); err != nil {
			return nil, err
		}
		key := filepath.Clean(h.Root)
		if seen[key] {
			continue
		}
		seen[key] = true
		roots = append(roots, h)
	}
	return roots, rows.Err()
}

// harnessMemoryRoot returns the memory root registered for harness, or "" if
// there is none.
func harnessMemoryRoot(ctx context.Context, db *sql.DB, harness string) (string, error) {
	var root string
	err := db.QueryRowContext(ctx,
		`SELECT memory_root FROM harnesses WHERE name = ?`, harness).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}

// harnessForRoot returns the most recently seen harness registered on root,
// or "" if there is none.
func harnessForRoot(ctx context.Context, db *sql.DB, root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", nil
	}
	var name string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM harnesses WHERE memory_root IN (?, ?)
		  ORDER BY last_seen_at DESC LIMIT 1`, filepath.Clean(abs), filepath.Clean(abs)+"/").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
