package db

import (
	"beacon/go/schema"
	"beacon/go/util"
	"fmt"
	"strings"
)

func MarshalTriggerName(tg schema.Trigger) string {
	operation := tg.Operation
	table := tg.Table
	schema := tg.Schema
	return fmt.Sprintf("%s_%s_%s_%s", BeaconTriggerPrefix, schema, table, operation)
}

func UnmarshalTriggerName(triggerName string) (schema.Trigger, error) {
	trimmed := strings.TrimPrefix(triggerName, BeaconTriggerPrefix+"_")

	parts := strings.Split(trimmed, "_")

	if len(parts) != 3 {
		return schema.Trigger{}, fmt.Errorf("invalid trigger name: %s", triggerName)
	}

	tgSchema := parts[0]
	tgTable := parts[1]
	tgOperation := schema.IOperation(parts[2])

	if !util.Includes(schema.Operations, tgOperation) {
		return schema.Trigger{}, fmt.Errorf("invalid trigger name, invalid \"operation\" value: %v", tgOperation)
	}

	tg := schema.Trigger{
		Schema:    tgSchema,
		Table:     tgTable,
		Operation: tgOperation,
	}

	return tg, nil
}
func CompareTriggerIds(a schema.Trigger, b schema.Trigger) bool {
	return a.Schema == b.Schema && a.Table == b.Table && a.Operation == b.Operation
}
