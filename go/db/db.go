package db

import (
	"beacon/go/schema"
	"fmt"
)

type Connector interface {
	Initialize() error
	Subscribe(cb func(p EventPayload)) int
	ApplySchema(s schema.Schema) error
	GetTableNamesOnSchema(sch string) ([]string, error)
}

const BeaconPrefix = "__beacon"

var BeaconTriggerPrefix = fmt.Sprintf("%s_trigger", BeaconPrefix)
var BeaconEventName = fmt.Sprintf("%s_event", BeaconPrefix)
var TriggerProcedureName = fmt.Sprintf("%s_notify", BeaconPrefix)
var BeaconMetadataTableName = fmt.Sprintf("%s_metadata", BeaconPrefix)
var BeaconRetryTableName = fmt.Sprintf("%s_retry", BeaconPrefix)

type EventPayload struct {
	Definition schema.Definition
	Old        interface{}
	New        interface{}
}
