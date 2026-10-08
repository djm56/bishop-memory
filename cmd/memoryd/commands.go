package main

import (
	"fmt"
	"os"
	"path/filepath"

	"bishop-memory/internal/auth"
	"bishop-memory/internal/config"
	"bishop-memory/internal/store"
)

const usage = `usage:
  memoryd                      serve (configuration from the environment)
  memoryd keys add <name>      create an API key and print it once
  memoryd keys list            list key names
  memoryd keys revoke <name>   remove a key
  memoryd backup <file>        write a consistent copy of the database to <file>

The keys file is BISHOP_API_KEYS_FILE (default: api-keys beside DB_PATH).
Changes to it take effect without a restart.
`

// runCommand runs one operator command and returns the exit status.
func runCommand(cfg config.Config, args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "memoryd:", err)
		return 1
	}
	switch {
	case len(args) == 3 && args[0] == "keys" && args[1] == "add":
		key, err := auth.Add(cfg.APIKeysFile, args[2])
		if err != nil {
			return fail(err)
		}
		fmt.Printf("Created key %q in %s. It is shown once; store it now:\n\n%s\n", args[2], cfg.APIKeysFile, key)
		return 0
	case len(args) == 2 && args[0] == "keys" && args[1] == "list":
		keys, err := auth.NewStore(cfg.APIKeysFile, "")
		if err != nil {
			return fail(err)
		}
		names, err := keys.Names()
		if err != nil {
			return fail(err)
		}
		if len(names) == 0 {
			fmt.Printf("No keys in %s.\n", cfg.APIKeysFile)
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return 0
	case len(args) == 3 && args[0] == "keys" && args[1] == "revoke":
		if err := auth.Revoke(cfg.APIKeysFile, args[2]); err != nil {
			return fail(err)
		}
		fmt.Printf("Revoked %q.\n", args[2])
		return 0
	case len(args) == 2 && args[0] == "backup":
		return backup(cfg, args[1], fail)
	case len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Print(usage)
		return 0
	}
	fmt.Fprint(os.Stderr, usage)
	return 2
}

// backup writes a consistent copy of the database with VACUUM INTO, which is
// safe while memoryd is serving. The target must not exist.
func backup(cfg config.Config, target string, fail func(error) int) int {
	if _, err := os.Stat(target); err == nil {
		return fail(fmt.Errorf("%s already exists", target))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fail(err)
	}
	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if _, err := db.Exec(`VACUUM INTO ?`, target); err != nil {
		return fail(fmt.Errorf("backup to %s: %w", target, err))
	}
	fmt.Printf("Backed up %s to %s.\n", cfg.DatabasePath, target)
	return 0
}
