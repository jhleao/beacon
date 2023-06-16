package postgres

import (
	"beacon/go/db"
	"encoding/json"
	"fmt"
)

func getMetadataTableUpsertSql() string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %[1]s (
	id SERIAL PRIMARY KEY,
	trigger_name VARCHAR(100),
	meta JSONB
);
CREATE INDEX IF NOT EXISTS %[1]s_trigger_name_idx ON %[1]s (trigger_name);
	`, db.BeaconMetadataTableName)
}

func getMetadataDeleteSql(tg db.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	return fmt.Sprintf(`
	DELETE FROM %[1]s WHERE trigger_name = '%[2]s';
	`, db.BeaconMetadataTableName, triggerName)
}

func getMetadataSelectSql(tg db.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	return fmt.Sprintf(`
	SELECT meta FROM %[1]s WHERE trigger_name = '%[2]s';
	`, db.BeaconMetadataTableName, triggerName)
}

func getMetadataInsertSql(tg db.Trigger, meta db.TriggerMeta) string {
	triggerName := db.MarshalTriggerName(tg)
	jsonMeta, err := json.Marshal(meta)

	if err != nil {
		panic(err)
	}

	return fmt.Sprintf(`
	INSERT INTO %[1]s (trigger_name, meta) VALUES ('%[2]s', '%[3]s');
	`, db.BeaconMetadataTableName, triggerName, jsonMeta)
}

func getTriggerUpsertSql(tg db.Trigger) string {
	triggerName := db.MarshalTriggerName(tg)
	fullTableName := fmt.Sprintf("%s.%s", tg.Schema, tg.Table)

	return fmt.Sprintf(`
DO
$$
BEGIN
IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = '%[1]s') THEN
	CREATE TRIGGER %[1]s after update on %[2]s FOR EACH ROW
	EXECUTE PROCEDURE %[3]s();
END IF;
END
$$
	`, triggerName, fullTableName, db.TriggerProcedureName)
}

func getTriggerDeleteSql(tg db.Trigger) string {
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
			'operation', to_jsonb(TG_OP),
			'schema', TG_TABLE_SCHEMA,
			'table', TG_TABLE_NAME
		);
	
		payload = jsonb_set(payload, '{new}', to_jsonb(NEW), TRUE);
		payload = jsonb_set(payload, '{old}', to_jsonb(OLD), TRUE);
		
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

func getExistingTablesSql() string {
	return "SELECT table_name FROM information_schema.tables	WHERE table_schema = 'public' AND table_type = 'BASE TABLE'"
}
