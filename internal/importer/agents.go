package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AgentsDirFor returns the agent definition directory that sits beside a
// harness memory root: <checkout>/.claude/memory -> <checkout>/.claude/agents.
func AgentsDirFor(memoryRoot string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(memoryRoot)), "agents")
}

// SyncAgents upserts one crew row per agent definition (*.md) in dir. The
// agent's name — the frontmatter name, or the file name without .md — is the
// key, lower-cased and without a leading "@", so the same agent defined in
// several harnesses stays one row, carrying whichever definition was synced
// last. A missing dir is not an error: not every harness keeps agents beside
// its memory root.
func SyncAgents(db *sql.DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read agents dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		fields := frontmatter(string(content))
		name := CrewName(fields["name"])
		if name == "" {
			name = CrewName(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
		description := fields["description"]
		if _, err := db.Exec(
			`INSERT INTO crew (name, role, description, source_path) VALUES (?, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
			   role = excluded.role,
			   description = excluded.description,
			   source_path = excluded.source_path`,
			name, nullIfEmpty(roleFrom(description, name)), nullIfEmpty(description), path,
		); err != nil {
			return fmt.Errorf("upsert crew %s: %w", name, err)
		}
	}
	return nil
}

// CrewName normalises an agent reference to its crew key: "@Hicks",
// "hicks" and "bishopharnessopencode:hicks" are all "hicks".
func CrewName(agent string) string {
	agent = strings.TrimSpace(agent)
	if i := strings.LastIndex(agent, ":"); i >= 0 {
		agent = agent[i+1:]
	}
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(agent), "@"))
}

// frontmatter returns the top-level "key: value" pairs of a leading YAML
// frontmatter block, with surrounding quotes removed. Nested and list values
// are skipped; only scalar lines are needed here.
func frontmatter(content string) map[string]string {
	fields := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fields
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '-' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		fields[strings.TrimSpace(key)] = value
	}
	return fields
}

// roleFrom shortens a description to its first sentence, without a
// parenthesised copy of the agent's own name: "Code reviewer (apone). Reads
// diffs…" becomes "Code reviewer".
func roleFrom(description, name string) string {
	role := description
	if i := strings.Index(role, ". "); i >= 0 {
		role = role[:i]
	}
	role = strings.TrimSuffix(strings.TrimSpace(role), ".")
	role = strings.Replace(role, "("+name+")", "", 1)
	role = strings.ReplaceAll(strings.Join(strings.Fields(role), " "), " ,", ",")
	return role
}
