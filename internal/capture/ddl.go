// Package capture handles PostgreSQL trigger installation for change data capture.
package capture

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// QuoteIdent safely quotes a SQL identifier.
func QuoteIdent(s string) string {
	return pgx.Identifier{s}.Sanitize()
}

// QuoteLiteral safely quotes a SQL string literal.
func QuoteLiteral(s string) string {
	// Escape single quotes by doubling them
	escaped := strings.ReplaceAll(s, "'", "''")
	return "'" + escaped + "'"
}

// TriggerName returns the deterministic name for a beacon trigger.
func TriggerName(schema, table string) string {
	return fmt.Sprintf("beacon_capture_%s_%s", schema, table)
}

// TableRef identifies a table by schema and name.
type TableRef struct {
	Schema string
	Name   string
}

// String returns the qualified table name.
func (t TableRef) String() string {
	return t.Schema + "." + t.Name
}
