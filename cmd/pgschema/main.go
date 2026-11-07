// Command pgschema inspects a PostgreSQL schema read-only: tables,
// columns, keys, a safe query, a sample, and a schema diff.
//
// It never issues a write statement, never calls a model, and connects
// only to the DSN the user gives. See README.md for the full contract.
package main

import (
	"os"

	"github.com/daxpublicfirst-arch/pgschema/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
