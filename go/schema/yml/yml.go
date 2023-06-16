package yml

import (
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/schema"
	"beacon/go/util"
	"errors"

	"gopkg.in/yaml.v3"
)

type SchemaYml struct {
	Triggers []TriggerYml `yaml:"triggers"`
}

type TriggerYml struct {
	Schema      string `yaml:"schema"`
	Table       string `yaml:"table"`
	Operation   string `yaml:"operation"`
	TriggerType string `yaml:"type"`
	Url         string `yaml:"url"`
}

type YmlSchemaParser struct {
	db db.Connector
}

func NewYmlSchemaParser(db db.Connector) *YmlSchemaParser {
	return &YmlSchemaParser{
		db: db,
	}
}

func (h *YmlSchemaParser) ValidateAndParse(rawSchema []byte) (schema.Schema, error) {
	parsedYml := SchemaYml{}

	err := yaml.Unmarshal(rawSchema, &parsedYml)
	if err != nil {
		return schema.Schema{}, errors.New("invalid schema format")
	}

	err = h.validateSchema(parsedYml)
	if err != nil {
		return schema.Schema{}, err
	}

	parsed := h.parseSchema(parsedYml)

	return parsed, nil
}

func (h *YmlSchemaParser) validateSchema(yml SchemaYml) error {
	const errPrefix = "schema validation failed: "

	tableNames, err := h.db.GetAllTableNames()
	if err != nil {
		return err
	}

	for _, trigger := range yml.Triggers {
		if !util.MapIncludes(schema.TriggerTypeNames, trigger.TriggerType) {
			return errors.New(errPrefix + "invalid \"type\" value: " + trigger.TriggerType)
		}

		if !util.MapIncludes(common.DbOperationNames, trigger.Operation) {
			return errors.New(errPrefix + "invalid \"operation\" value: " + trigger.Operation)
		}

		if trigger.Table == "" {
			return errors.New(errPrefix + "table must be set")
		}

		if trigger.Schema == "" {
			return errors.New(errPrefix + "schema must be set")
		}

		if trigger.TriggerType == schema.TriggerTypeNames[schema.Http] && trigger.Url == "" {
			return errors.New(errPrefix + "url must be set for http triggers")
		}

		if !util.Includes(tableNames, trigger.Table) {
			return errors.New(errPrefix + "table \"" + trigger.Table + "\" does not exist")
		}
	}

	return nil
}

// Parses schema. Can panic in case of invalid schema. Validate before calling this.
func (h *YmlSchemaParser) parseSchema(yml SchemaYml) schema.Schema {
	parsed := schema.Schema{}

	for _, rawTrigger := range yml.Triggers {
		trigger := schema.Trigger{}

		operation, err := util.FindMapKeyByValue(common.DbOperationNames, rawTrigger.Operation)
		if err != nil {
			panic(err)
		}

		triggerType, err := util.FindMapKeyByValue(schema.TriggerTypeNames, rawTrigger.TriggerType)
		if err != nil {
			panic(err)
		}

		trigger.Operation = operation
		trigger.Table = rawTrigger.Table
		trigger.Schema = rawTrigger.Schema
		trigger.TriggerType = triggerType
		trigger.Config = make(map[string]string)

		if trigger.TriggerType == schema.Http {
			trigger.Config["url"] = rawTrigger.Url
		}

		parsed.Triggers = append(parsed.Triggers, trigger)
	}

	return parsed
}
