package postgres

import (
	"beacon/go/db"
	"beacon/go/schema"
	"encoding/json"
	"fmt"
)

func getMetadataTableUpsertSql() string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %[1]s (
	id SERIAL PRIMARY KEY,
	trigger_name VARCHAR(100),
	action JSONB
);
CREATE INDEX IF NOT EXISTS %[1]s_trigger_name_idx ON %[1]s (trigger_name);
	`, db.BeaconMetadataTableName)
}

func getRetryTableUpsertSql() string {
	return fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %[1]s (
		id SERIAL PRIMARY KEY,
		try_count INTEGER NOT NULL,
		event_payload JSONB NOT NULL,
		deferred_at TIMESTAMP,
		last_try_at TIMESTAMP,
		last_response TEXT
	);
	CREATE INDEX IF NOT EXISTS %[1]s_try_count_idx ON %[1]s (try_count);
	`, db.BeaconRetryTableName)
}

func getActionDeleteSql(tg schema.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	return fmt.Sprintf(`
	DELETE FROM %[1]s WHERE trigger_name = '%[2]s';
	`, db.BeaconMetadataTableName, triggerName)
}

func getActionSelectSql(tg schema.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	return fmt.Sprintf(`
	SELECT action FROM %[1]s WHERE trigger_name = '%[2]s';
	`, db.BeaconMetadataTableName, triggerName)
}

func getActionInsertSql(triggerName string, action schema.Action) string {
	jsonAction, err := json.Marshal(action)

	if err != nil {
		panic(err)
	}

	return fmt.Sprintf(`
	INSERT INTO %[1]s (trigger_name, action) VALUES ('%[2]s', '%[3]s');
	`, db.BeaconMetadataTableName, triggerName, jsonAction)
}

func getTriggerUpsertSql(tg schema.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	fullTableName := fmt.Sprintf("\"%s\".\"%s\"", tg.Schema, tg.Table)

	return fmt.Sprintf(`
DO
$$
BEGIN
IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = '%[1]s') THEN
	CREATE TRIGGER %[1]s AFTER %[4]s ON %[2]s FOR EACH ROW
	EXECUTE PROCEDURE %[3]s();
END IF;
END
$$
	`, triggerName, fullTableName, db.TriggerProcedureName, tg.Operation)
}

func getTriggerDeleteSql(tg schema.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	fullTableName := fmt.Sprintf("%s.%s", tg.Schema, tg.Table)

	return fmt.Sprintf(`
DO
$$
BEGIN
IF EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = '%[1]s') THEN
	DROP TRIGGER %[1]s on %[2]s;
END IF;
END
$$
	`, triggerName, fullTableName)
}

func getGlobalProcedureUpsertSql() string {
	return fmt.Sprintf(`
DO
$$
BEGIN
IF NOT EXISTS(SELECT 1 FROM pg_proc WHERE proname = '%[1]s') THEN
	CREATE OR REPLACE FUNCTION %[1]s() RETURNS TRIGGER AS
	$FN$
	DECLARE
		payload JSONB;
	BEGIN
		payload = jsonb_build_object(
			'schema', TG_TABLE_SCHEMA,
			'table', TG_TABLE_NAME,
			'operation', to_jsonb(TG_OP)
		);

		IF TG_OP = 'INSERT' THEN
			payload = jsonb_set(payload, '{new}', to_jsonb(NEW), TRUE);
		ELSIF TG_OP = 'DELETE' THEN
			payload = jsonb_set(payload, '{old}', to_jsonb(OLD), TRUE);
		ELSIF TG_OP = 'UPDATE' THEN
			payload = jsonb_set(payload, '{old}', to_jsonb(OLD), TRUE);
			payload = jsonb_set(payload, '{new}', to_jsonb(NEW), TRUE);
		END IF;
		
		PERFORM pg_notify('%[2]s', payload::TEXT);
		
		RETURN NEW;
	END;
	$FN$ LANGUAGE plpgsql;
END IF;
END
$$
	`, db.TriggerProcedureName, db.BeaconEventName)
}

func getExistingTriggersSql() string {
	return fmt.Sprintf("SELECT tgname FROM pg_trigger WHERE tgname LIKE '%s%%'", db.BeaconPrefix)
}

func getExistingTablesSql(sch string) string {
	return fmt.Sprintf("SELECT table_name FROM information_schema.tables	WHERE table_schema = '%[1]s' AND table_type = 'BASE TABLE'", sch)
}
