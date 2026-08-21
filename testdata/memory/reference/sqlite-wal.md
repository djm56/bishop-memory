# Reference: SQLite WAL Mode

WAL (Write-Ahead Logging) mode is enabled at the connection layer via
the `_pragma=journal_mode(WAL)` parameter in the DSN. Readers do not
block the writer, and the single-writer pattern (SetMaxOpenConns(1))
serialises writes so concurrent HTTP handlers never trip
"database is locked".

This file exercises the `reference/` kind mapping.
