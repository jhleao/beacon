package postgres

import (
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/schema"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type PostgresSuite struct {
	suite.Suite
	pgx *pgx.Conn
	sut PostgresConnector
	uri string
}

func (suite *PostgresSuite) SetupSuite() {
	suite.uri = os.Getenv("POSTGRES_TEST_URI")
	if suite.uri == "" {
		suite.FailNow("POSTGRES_TEST_URI not set")
	}

	connCfg, err := pgx.ParseURI(suite.uri)
	if err != nil {
		suite.FailNow("POSTGRES_TEST_URI is invalid")
	}

	pgx, err := pgx.Connect(connCfg)
	if err != nil {
		suite.FailNow("Could not connect to postgres")
	}

	suite.pgx = pgx

	suite.sut = *NewPostgresConnector(suite.uri, common.NewShutdown())

	err = suite.sut.Initialize()
	if err != nil {
		suite.FailNow("Could not initialize connector")
	}
}

func (suite *PostgresSuite) SetupTest() {
	suite.cleanupDb()
}

func (suite *PostgresSuite) cleanupDb() {
	// Cleanup schema
	suite.sut.ApplySchema(schema.Schema{})

	// Cleanup tables
	_, err := suite.pgx.Exec(`
DO $$ DECLARE
	"tableName" TEXT;
BEGIN
	FOR "tableName" IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
			EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident("tableName") || ' CASCADE';
	END LOOP;
END $$;
`)

	if err != nil {
		suite.FailNow("Could not cleanup tables")
	}

	// Cleanup triggers
	_, err = suite.pgx.Exec(fmt.Sprintf(`
DO
$$
DECLARE
    trigger_record RECORD;
BEGIN
    FOR trigger_record IN (
        SELECT tgname
        FROM pg_trigger
        WHERE tgname LIKE '%s%%'
    ) LOOP
        EXECUTE 'DROP TRIGGER ' || trigger_record.tgname;
    END LOOP;
END
$$;
`, db.BeaconPrefix))

	if err != nil {
		suite.FailNow("Could not cleanup triggers")
	}

	// Cleanup functions
	_, err = suite.pgx.Exec(fmt.Sprintf(`
DO
$$
DECLARE
		function_record RECORD;
BEGIN
		FOR function_record IN (
				SELECT proname
				FROM pg_proc
				WHERE proname LIKE '%s%%'
		) LOOP
				EXECUTE 'DROP FUNCTION ' || function_record.proname;
		END LOOP;	
END
$$;
`, db.BeaconPrefix))

	if err != nil {
		suite.FailNow("Could not cleanup functions")
	}

}

func TestPostgresConnectorSuite(t *testing.T) {
	suite.Run(t, new(PostgresSuite))
}

func (suite *PostgresSuite) TestGetTableNamesOnSchema() {
	suite.pgx.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`)
	suite.pgx.Exec(`CREATE TABLE posts (id SERIAL PRIMARY KEY, name TEXT);`)

	tableNames, err := suite.sut.GetTableNamesOnSchema("public")

	assert.NoError(suite.T(), err)
	assert.ElementsMatch(suite.T(), []string{"posts", "users"}, tableNames)
}

func (suite *PostgresSuite) TestApplySchema() {
	tg := schema.Trigger{
		Schema:    "public",
		Table:     "users",
		Operation: schema.Operation.Insert,
	}

	act := schema.Action{
		Type:   schema.ActionType.Http,
		Method: schema.HttpMethod.Post,
		Url:    "http://localhost:8080/stub",
	}

	sch := schema.Schema{
		Definitions: []schema.Definition{
			{
				Trigger: tg,
				Action:  act,
			},
		},
	}

	suite.pgx.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`)

	err := suite.sut.ApplySchema(sch)
	assert.NoError(suite.T(), err)

	// Verify trigger was created
	expectedTgName := db.MarshalTriggerName(tg)
	rows, err := suite.pgx.Query(`SELECT tgname FROM pg_trigger WHERE tgname = $1`, expectedTgName)
	assert.NoError(suite.T(), err)
	existingTgNames := []string{}
	for rows.Next() {
		var trigger_name string
		err := rows.Scan(&trigger_name)
		assert.NoError(suite.T(), err)
		existingTgNames = append(existingTgNames, trigger_name)
	}
	rows.Close()
	assert.ElementsMatch(suite.T(), []string{expectedTgName}, existingTgNames)

	// Verify function was created
	rows, err = suite.pgx.Query(`SELECT proname FROM pg_proc WHERE proname = $1`, db.TriggerProcedureName)
	assert.NoError(suite.T(), err)
	existingFnNames := []string{}
	for rows.Next() {
		var trigger_name string
		err := rows.Scan(&trigger_name)
		assert.NoError(suite.T(), err)
		existingFnNames = append(existingFnNames, trigger_name)
	}
	rows.Close()
	assert.ElementsMatch(suite.T(), []string{db.TriggerProcedureName}, existingFnNames)

	// Verify metadata was created
	rows, err = suite.pgx.Query(fmt.Sprintf(`SELECT trigger_name FROM %s WHERE trigger_name = $1`, db.BeaconMetadataTableName), expectedTgName)
	assert.NoError(suite.T(), err)
	existingMetadata := []string{}
	for rows.Next() {
		var trigger_name string
		err := rows.Scan(&trigger_name)
		assert.NoError(suite.T(), err)
		existingMetadata = append(existingMetadata, trigger_name)
	}
	rows.Close()
	assert.ElementsMatch(suite.T(), []string{expectedTgName}, existingMetadata)

	// Empty schema
	err = suite.sut.ApplySchema(schema.Schema{})
	assert.NoError(suite.T(), err)

	// Verify trigger was deleted
	expectedTgName = db.MarshalTriggerName(tg)
	rows, err = suite.pgx.Query(`SELECT tgname FROM pg_trigger WHERE tgname = $1`, expectedTgName)
	assert.NoError(suite.T(), err)
	existingTgNames = []string{}
	for rows.Next() {
		var trigger_name string
		err := rows.Scan(&trigger_name)
		assert.NoError(suite.T(), err)
		existingTgNames = append(existingTgNames, trigger_name)
	}
	rows.Close()
	assert.Equal(suite.T(), len(existingTgNames), 0)

	// Verify metadata was deleted
	rows, err = suite.pgx.Query(fmt.Sprintf(`SELECT trigger_name FROM %s WHERE trigger_name = $1`, db.BeaconMetadataTableName), expectedTgName)
	assert.NoError(suite.T(), err)
	existingMetadata = []string{}
	for rows.Next() {
		var trigger_name string
		err := rows.Scan(&trigger_name)
		assert.NoError(suite.T(), err)
		existingMetadata = append(existingMetadata, trigger_name)
	}
	rows.Close()
	assert.Equal(suite.T(), len(existingMetadata), 0)
}

func (suite *PostgresSuite) TestApplySchemaAndNotify_Update() {
	tg := schema.Trigger{
		Schema:    "public",
		Table:     "users",
		Operation: schema.Operation.Update,
	}

	act := schema.Action{
		Type:   schema.ActionType.Http,
		Method: schema.HttpMethod.Post,
		Url:    "http://localhost:8080/stub",
	}

	sch := schema.Schema{
		Definitions: []schema.Definition{
			{
				Trigger: tg,
				Action:  act,
			},
		},
	}

	_, err := suite.pgx.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`)
	assert.NoError(suite.T(), err)

	receivedPayloads := []db.EventPayload{}
	cb := func(p db.EventPayload) {
		receivedPayloads = append(receivedPayloads, p)
	}

	// Initializing new connector to pass a separate shutdown channel
	sh := common.NewShutdown()
	defer sh.Shutdown()
	conn := NewPostgresConnector(suite.uri, sh)

	err = conn.Initialize()
	assert.NoError(suite.T(), err)

	conn.Subscribe(cb)

	err = conn.ApplySchema(sch)
	assert.NoError(suite.T(), err)

	_, err = suite.pgx.Exec(`INSERT INTO users (name) VALUES ('John');`)
	assert.NoError(suite.T(), err)
	_, err = suite.pgx.Exec(`UPDATE users SET name = 'Joe' WHERE name = 'John';`)
	assert.NoError(suite.T(), err)

	time.Sleep(500 * time.Millisecond) // Give some time for the event to be processed

	assert.Equal(suite.T(), 1, len(receivedPayloads))
	receivedName := receivedPayloads[0].New.(map[string]interface{})["name"]
	assert.Equal(suite.T(), "Joe", receivedName)
}

func (suite *PostgresSuite) TestApplySchemaAndNotify_Insert() {
	tg := schema.Trigger{
		Schema:    "public",
		Table:     "users",
		Operation: schema.Operation.Insert,
	}

	act := schema.Action{
		Type:   schema.ActionType.Http,
		Method: schema.HttpMethod.Post,
		Url:    "http://localhost:8080/stub",
	}

	sch := schema.Schema{
		Definitions: []schema.Definition{
			{
				Trigger: tg,
				Action:  act,
			},
		},
	}

	_, err := suite.pgx.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`)
	assert.NoError(suite.T(), err)

	receivedPayloads := []db.EventPayload{}
	cb := func(p db.EventPayload) {
		receivedPayloads = append(receivedPayloads, p)
	}

	// Initializing new connector to pass a separate shutdown channel
	sh := common.NewShutdown()
	defer sh.Shutdown()
	conn := NewPostgresConnector(suite.uri, sh)

	err = conn.Initialize()
	assert.NoError(suite.T(), err)

	conn.Subscribe(cb)

	err = conn.ApplySchema(sch)
	assert.NoError(suite.T(), err)

	_, err = suite.pgx.Exec(`INSERT INTO users (name) VALUES ('John');`)
	assert.NoError(suite.T(), err)

	time.Sleep(1 * time.Second) // Give some time for the event to be processed

	assert.Equal(suite.T(), 1, len(receivedPayloads))
	receivedName := receivedPayloads[0].New.(map[string]interface{})["name"]
	assert.Equal(suite.T(), "John", receivedName)
}

func (suite *PostgresSuite) TestApplySchemaAndNotify_Delete() {
	tg := schema.Trigger{
		Schema:    "public",
		Table:     "users",
		Operation: schema.Operation.Delete,
	}

	act := schema.Action{
		Type:   schema.ActionType.Http,
		Method: schema.HttpMethod.Post,
		Url:    "http://localhost:8080/stub",
	}

	sch := schema.Schema{
		Definitions: []schema.Definition{
			{
				Trigger: tg,
				Action:  act,
			},
		},
	}

	_, err := suite.pgx.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`)
	assert.NoError(suite.T(), err)

	receivedPayloads := []db.EventPayload{}
	cb := func(p db.EventPayload) {
		receivedPayloads = append(receivedPayloads, p)
	}

	// Initializing new connector to pass a separate shutdown channel
	sh := common.NewShutdown()
	defer sh.Shutdown()
	conn := NewPostgresConnector(suite.uri, sh)

	err = conn.Initialize()
	assert.NoError(suite.T(), err)

	conn.Subscribe(cb)

	err = conn.ApplySchema(sch)
	assert.NoError(suite.T(), err)

	_, err = suite.pgx.Exec(`INSERT INTO users (name) VALUES ('John');`)
	assert.NoError(suite.T(), err)
	_, err = suite.pgx.Exec(`DELETE FROM users WHERE name = 'John';`)
	assert.NoError(suite.T(), err)

	time.Sleep(1 * time.Second) // Give some time for the event to be processed

	assert.Equal(suite.T(), 1, len(receivedPayloads))
	receivedName := receivedPayloads[0].Old.(map[string]interface{})["name"]
	assert.Equal(suite.T(), "John", receivedName)
}
