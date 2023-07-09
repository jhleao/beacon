package yml

import (
	"beacon/go/db/mockdb"
	"beacon/go/schema"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type YmlSchemaParserSuite struct {
	suite.Suite
	mockdb *mockdb.MockConnector
	parser *YmlSchemaParser
}

func (suite *YmlSchemaParserSuite) SetupSuite() {
	mockDb := mockdb.MockConnector{}
	suite.mockdb = &mockDb
	suite.parser = NewYmlSchemaParser(&mockDb)
}

func (suite *YmlSchemaParserSuite) TearDownTest() {
	suite.mockdb.ExpectedCalls = nil
}

func TestYmlSchemaParserSuite(t *testing.T) {
	suite.Run(t, new(YmlSchemaParserSuite))
}

func (suite *YmlSchemaParserSuite) TestValidateAndParse_ValidSchema() {
	stubSchema := []byte(`
version: 1

definitions:
  - trigger:
      table: user
      operation: insert
    action:
      type: http
      method: post
      url: http://localhost:3000/users
`)

	suite.mockdb.On("GetTableNamesOnSchema", mock.Anything).
		Return([]string{"user"}, nil).Once()

	parsedSchema, err := suite.parser.ValidateAndParse(stubSchema)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), parsedSchema)
}

// Test ValidateAndParse with an invalid schema format
func (suite *YmlSchemaParserSuite) TestValidateAndParse_InvalidSchemaFormat() {
	rawSchema := []byte(`invalid_schema_format`)

	_, err := suite.parser.ValidateAndParse(rawSchema)
	assert.EqualError(suite.T(), err, "invalid schema format")
}

func (suite *YmlSchemaParserSuite) TestValidateAndParse_UnsupportedSchemaVersion() {
	rawSchema := []byte(`
    version: 2
    definitions:
      - trigger:
          schema: public
          table: users
          operation: INSERT
        action:
          type: HTTP
          url: https://example.com
          method: POST
  `)

	_, err := suite.parser.ValidateAndParse(rawSchema)
	assert.EqualError(suite.T(), err, "schema validation failed: unsupported version. Supported: '1'")
}

func (suite *YmlSchemaParserSuite) TestValidateAndParse_InvalidOperation() {
	rawSchema := []byte(`
    version: 1
    definitions:
      - trigger:
          schema: public
          table: users
          operation: INSERT
        action:
          type: http
          url: https://example.com
          method: POST
  `)

	_, err := suite.parser.ValidateAndParse(rawSchema)
	assert.EqualError(suite.T(), err, "schema validation failed:  invalid \"operation\" value: INSERT")
}

func (suite *YmlSchemaParserSuite) TestValidateAndParse_UnexistingTable() {
	rawSchema := []byte(`
    version: 1
    definitions:
      - trigger:
          schema: public
          table: users
          operation: insert
        action:
          type: http
          url: https://example.com
          method: post
  `)

	suite.mockdb.On("GetTableNamesOnSchema", mock.Anything).
		Return([]string{"sometable"}, nil).Once()

	_, err := suite.parser.ValidateAndParse(rawSchema)
	assert.EqualError(suite.T(), err, "schema validation failed:  table \"users\" does not exist on relation public")
}

func (suite *YmlSchemaParserSuite) TestBackfillSchema() {
	yml := schema.Schema{
		Version: 1,
		Definitions: []schema.Definition{
			{
				Trigger: schema.Trigger{
					Schema:    "",
					Table:     "users",
					Operation: "INSERT",
				},
				Action: schema.Action{
					Type:   "",
					Method: "",
				},
			},
		},
	}

	suite.parser.backfillSchema(yml)

	// Verify the default values are filled
	assert.Equal(suite.T(), "public", yml.Definitions[0].Trigger.Schema)
	assert.Equal(suite.T(), schema.ActionType.Http, yml.Definitions[0].Action.Type)
	assert.Equal(suite.T(), schema.HttpMethod.Post, yml.Definitions[0].Action.Method)
}
