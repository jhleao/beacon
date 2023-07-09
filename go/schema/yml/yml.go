package yml

import (
	"beacon/go/db"
	"beacon/go/schema"
	"beacon/go/util"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

type YmlSchemaParser struct {
	db db.Connector
}

func NewYmlSchemaParser(db db.Connector) *YmlSchemaParser {
	return &YmlSchemaParser{
		db: db,
	}
}

func (h *YmlSchemaParser) ValidateAndParse(rawSchema []byte) (schema.Schema, error) {
	parsed := schema.Schema{}

	err := yaml.Unmarshal(rawSchema, &parsed)
	if err != nil {
		return schema.Schema{}, errors.New("invalid schema format")
	}

	h.backfillSchema(parsed)

	err = h.validateSchema(parsed)
	if err != nil {
		return schema.Schema{}, err
	}

	return parsed, nil
}

func (h *YmlSchemaParser) validateSchema(yml schema.Schema) error {
	const errPrefix = "schema validation failed: "

	if yml.Version != 1 {
		return errors.New(errPrefix + "unsupported version. Supported: '1'")
	}

	if yml.Driver != "postgres" {
		return errors.New(errPrefix + "unsupported driver. Supported: 'postgres'")
	}

	for _, definition := range yml.Definitions {
		if !util.Includes(schema.Operations, schema.IOperation(definition.Trigger.Operation)) {
			return fmt.Errorf("%s invalid \"operation\" value: %v", errPrefix, definition.Trigger.Operation)
		}

		if definition.Trigger.Table == "" {
			return errors.New(errPrefix + "table must be set")
		}

		if !util.Includes(schema.ActionTypes, schema.IActionType(definition.Action.Type)) {
			return fmt.Errorf("%s invalid \"action.type\" value: %v", errPrefix, definition.Action.Type)
		}

		if schema.IActionType(definition.Action.Type) == schema.ActionType.Http {
			if definition.Action.Url == "" {
				return errors.New(errPrefix + "url must be set for http actions")
			}
			if definition.Action.Method == "" {
				return errors.New(errPrefix + "method must be set for http actions")
			}
			if !util.Includes(schema.HttpMethods, schema.IHttpMethod(definition.Action.Method)) {
				return fmt.Errorf("%s invalid \"action.method\" value: %v", errPrefix, definition.Action.Method)
			}
		}

		tableNames, err := h.db.GetTableNamesOnSchema(definition.Trigger.Schema)

		if err != nil {
			return err
		}

		if !util.Includes(tableNames, definition.Trigger.Table) {
			return fmt.Errorf("%s table \"%s\" does not exist on relation %s", errPrefix, definition.Trigger.Table, definition.Trigger.Schema)
		}
	}

	return nil
}

// Backfills default values for schema
func (h *YmlSchemaParser) backfillSchema(yml schema.Schema) {
	for i, definition := range yml.Definitions {
		if definition.Trigger.Schema == "" {
			yml.Definitions[i].Trigger.Schema = "public"
		}

		if definition.Action.Type == "" {
			yml.Definitions[i].Action.Type = schema.ActionType.Http
		}

		if definition.Action.Method == "" {
			yml.Definitions[i].Action.Method = schema.HttpMethod.Post
		}
	}
}
