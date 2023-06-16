package schema

import "beacon/go/common"

type TriggerType int

const (
	Http TriggerType = iota
)

type Parser interface {
	ValidateAndParse(rawSchema []byte) (Schema, error)
}

type Schema struct {
	Triggers []Trigger
}

type Trigger struct {
	Schema      string
	Table       string
	Operation   common.DbOperation
	TriggerType TriggerType
	Config      map[string]string
}

var TriggerTypeNames = map[TriggerType]string{
	Http: "http",
}
