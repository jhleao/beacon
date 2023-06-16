package db

import (
	"beacon/go/common"
	"beacon/go/schema"
	"fmt"
)

type EventPayload struct {
	Actions   []TriggerMeta
	Operation string      `json:"operation"`
	Schema    string      `json:"schema"`
	Table     string      `json:"table"`
	Old       interface{} `json:"old"`
	New       interface{} `json:"new"`
}

type Trigger struct {
	Schema    string
	Table     string
	Operation common.DbOperation
}

type TriggerMeta struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
}

type Connector interface {
	Initialize() error
	Subscribe(cb func(p EventPayload)) int
	ApplySchema(s schema.Schema) error
	GetAllTableNames() ([]string, error)
}

const BeaconPrefix = "__beacon"

var BeaconTriggerPrefix = fmt.Sprintf("%s_trigger", BeaconPrefix)
var BeaconEventName = fmt.Sprintf("%s_event", BeaconPrefix)
var TriggerProcedureName = fmt.Sprintf("%s_trigger_procedure", BeaconPrefix)
var BeaconMetadataTableName = fmt.Sprintf("%s_metadata", BeaconPrefix)
