package capture

import (
	"context"
	"fmt"

	"beacon/internal/db"
)

// Installer manages trigger installation on user tables.
type Installer struct {
	pool *db.Pool
}

// New creates an Installer.
func New(pool *db.Pool) *Installer {
	return &Installer{pool: pool}
}

// InstallTrigger creates the beacon trigger on a table (idempotent).
func (i *Installer) InstallTrigger(ctx context.Context, schema, table string) error {
	triggerName := TriggerName(schema, table)

	// Build the CREATE TRIGGER statement
	createSQL := fmt.Sprintf(
		`CREATE TRIGGER %s AFTER INSERT OR UPDATE OR DELETE ON %s.%s FOR EACH ROW EXECUTE FUNCTION beacon.capture_changes()`,
		QuoteIdent(triggerName),
		QuoteIdent(schema),
		QuoteIdent(table),
	)

	// Use DO block for IF NOT EXISTS semantics
	_, err := i.pool.Exec(ctx, fmt.Sprintf(`
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_trigger
				WHERE tgname = %s
			) THEN
				EXECUTE %s;
			END IF;
		END $$
	`, QuoteLiteral(triggerName), QuoteLiteral(createSQL)))

	if err != nil {
		return fmt.Errorf("install trigger on %s.%s: %w", schema, table, err)
	}

	return nil
}

// UninstallTrigger removes the beacon trigger from a table.
func (i *Installer) UninstallTrigger(ctx context.Context, schema, table string) error {
	triggerName := TriggerName(schema, table)

	_, err := i.pool.Exec(ctx, fmt.Sprintf(
		`DROP TRIGGER IF EXISTS %s ON %s.%s`,
		QuoteIdent(triggerName),
		QuoteIdent(schema),
		QuoteIdent(table),
	))

	if err != nil {
		return fmt.Errorf("uninstall trigger from %s.%s: %w", schema, table, err)
	}

	return nil
}

// ListTriggers returns all tables with beacon triggers installed.
func (i *Installer) ListTriggers(ctx context.Context) ([]TableRef, error) {
	rows, err := i.pool.Query(ctx, `
		SELECT
			n.nspname AS schema_name,
			c.relname AS table_name
		FROM pg_trigger t
		JOIN pg_class c ON t.tgrelid = c.oid
		JOIN pg_namespace n ON c.relnamespace = n.oid
		WHERE t.tgname LIKE 'beacon_capture_%'
		  AND NOT t.tgisinternal
	`)
	if err != nil {
		return nil, fmt.Errorf("list triggers: %w", err)
	}
	defer rows.Close()

	var triggers []TableRef
	for rows.Next() {
		var ref TableRef
		if err := rows.Scan(&ref.Schema, &ref.Name); err != nil {
			return nil, fmt.Errorf("scan trigger: %w", err)
		}
		triggers = append(triggers, ref)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate triggers: %w", err)
	}

	return triggers, nil
}

// ListTriggersMap returns a map of tables with beacon triggers for quick lookup.
func (i *Installer) ListTriggersMap(ctx context.Context) (map[TableRef]bool, error) {
	triggers, err := i.ListTriggers(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[TableRef]bool)
	for _, t := range triggers {
		result[t] = true
	}
	return result, nil
}
