// Package pg implements the read-only PostgreSQL access layer for pgschema:
// connections, SQL validation, schema inspection, querying, sampling and
// diffing. It never issues a write statement against the target database.
package pg

// Version is the tool version, kept in one place and reported by --version,
// in JSON output and in telemetry.
const Version = "0.1.0"

// ToolName is the binary / tool name used in JSON output and telemetry.
const ToolName = "pgschema"
