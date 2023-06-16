package db

import (
	"beacon/go/common"
	"beacon/go/log"
	"beacon/go/schema"
	"beacon/go/util"
	"errors"
	"fmt"
	"strings"
)

func MarshalTriggerName(tg Trigger) string {
	operation := tg.Operation
	table := tg.Table
	schema := tg.Schema
	operationName := common.DbOperationNames[operation]
	return fmt.Sprintf("%s_%s_%s_%s", BeaconTriggerPrefix, schema, table, operationName)
}

func UnmarshalTriggerId(triggerName string) (Trigger, error) {
	trimmed := strings.TrimPrefix(triggerName, BeaconTriggerPrefix+"_")

	parts := strings.Split(trimmed, "_")

	if len(parts) != 3 {
		return Trigger{}, errors.New("Invalid trigger name: " + triggerName)
	}

	operation, err := util.FindMapKeyByValue(common.DbOperationNames, parts[2])

	if err != nil {
		return Trigger{}, errors.New("Invalid trigger name: " + triggerName)
	}

	triggerID := Trigger{
		Schema:    parts[0],
		Table:     parts[1],
		Operation: operation,
	}

	return triggerID, nil
}

func GetTriggersFromSchema(s schema.Schema) []Trigger {
	tgs := []Trigger{}
	for _, trigger := range s.Triggers {
		tg := TriggerFromSchemaTrigger(trigger)
		tgs = append(tgs, tg)
	}
	return tgs
}

// TODO this should be removed with some refactoring on these interfaces
func TriggerFromSchemaTrigger(tg schema.Trigger) Trigger {
	return Trigger{
		Operation: tg.Operation,
		Table:     tg.Table,
		Schema:    tg.Schema,
	}
}

func GetTriggerMetaFromSchema(s schema.Schema, tg Trigger) (TriggerMeta, error) {
	for _, schemaTg := range s.Triggers {
		parsedSchemaTg := TriggerFromSchemaTrigger(schemaTg)
		if CompareTriggerIds(tg, parsedSchemaTg) {
			typeName := schema.TriggerTypeNames[schemaTg.TriggerType]
			return TriggerMeta{
				Type:   typeName,
				Config: schemaTg.Config,
			}, nil
		}
	}

	return TriggerMeta{}, errors.New("Trigger not found in schema")
}

func CompareTriggerIds(a Trigger, b Trigger) bool {
	return a.Schema == b.Schema && a.Table == b.Table && a.Operation == b.Operation
}

func TriggerFromEventPayload(p EventPayload) (Trigger, error) {
	operation, err := util.FindMapKeyByValue(common.DbOperationNames, strings.ToLower(p.Operation))

	if err != nil {
		log.Error("invalid operation name from event payload", "error", err, "operation", p.Operation)
		return Trigger{}, err
	}

	tg := Trigger{
		Schema:    p.Schema,
		Table:     p.Table,
		Operation: operation,
	}

	return tg, nil
}
