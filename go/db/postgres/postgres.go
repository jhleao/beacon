package postgres

import (
	"beacon/go/blog"
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/retry"
	"beacon/go/schema"
	"beacon/go/util"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx"
)

type PostgresConnector struct {
	uri      string
	shutdown common.Shutdown
	pool     *pgx.ConnPool
	ps       *util.PubSub[db.EventPayload]
}

func NewPostgresConnector(uri string, shutdown common.Shutdown) *PostgresConnector {
	return &PostgresConnector{
		uri:      uri,
		shutdown: shutdown,
		ps:       util.NewPubSub[db.EventPayload](),
	}
}

func (c *PostgresConnector) Initialize() error {
	connCfg, err := pgx.ParseURI(c.uri)

	if err != nil {
		return errors.New("uri must be set and valid for PostgresConnector")
	}

	c.pool, err = pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:     connCfg,
		MaxConnections: 2,
		AcquireTimeout: 30 * time.Second,
	})

	if err != nil {
		return fmt.Errorf("could not initialize Postgres connection: %v", err.Error())
	}

	blog.Info("Connected to Postgres")

	c.shutdown.WaitGroup.Add(1)
	go c.listen()

	return nil
}

func (c *PostgresConnector) Subscribe(cb func(payload db.EventPayload)) int {
	return c.ps.Subscribe(cb)
}

func (c *PostgresConnector) ApplySchema(sch schema.Schema) error {
	existingTriggers, err := c.getExistingTriggers()

	if err != nil {
		return err
	}

	triggersToUpsert := []schema.Trigger{}
	for _, def := range sch.Definitions {
		triggersToUpsert = append(triggersToUpsert, def.Trigger)
	}

	triggersToDelete := []schema.Trigger{}
	for _, existingTrigger := range existingTriggers {
		shouldDelete := true
		for _, schemaTrigger := range triggersToUpsert {
			if db.CompareTriggerIds(existingTrigger, schemaTrigger) {
				shouldDelete = false
				break
			}
		}
		if shouldDelete {
			triggersToDelete = append(triggersToDelete, existingTrigger)
		}

	}

	actionsToUpsert := map[string]schema.Action{}
	for _, def := range sch.Definitions {
		tn := db.MarshalTriggerName(def.Trigger)
		actionsToUpsert[tn] = def.Action
	}

	conn, err := c.pool.Acquire()

	if err != nil {
		blog.Error("Could not acquire connection to apply schema", "error", err)
		return err
	}

	defer c.pool.Release(conn)

	tx, err := conn.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback()

	if _, err = tx.Exec(getMetadataTableUpsertSql()); err != nil {
		return fmt.Errorf("could not upsert metadata table: %v", err.Error())
	}

	if _, err = tx.Exec(getRetryTableUpsertSql()); err != nil {
		return fmt.Errorf("could not upsert retries table: %v", err.Error())
	}

	if _, err = tx.Exec(getGlobalProcedureUpsertSql()); err != nil {
		return fmt.Errorf("could not upsert global procedure: %v", err.Error())
	}

	for _, triggerId := range triggersToDelete {
		if _, err = tx.Exec(getTriggerDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger: %v", err.Error())
		}

		if _, err = tx.Exec(getActionDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger metadata: %v", err.Error())
		}
	}

	for _, triggerId := range triggersToUpsert {
		if _, err = tx.Exec(getTriggerUpsertSql(triggerId)); err != nil {
			return fmt.Errorf("could not upsert trigger: %v", err.Error())
		}

		// This just recreates all metadata for the trigger. Can be improve to diffing
		if _, err = tx.Exec(getActionDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger metadata: %v", err.Error())
		}
	}

	for triggerId, action := range actionsToUpsert {
		if _, err = tx.Exec(getActionInsertSql(triggerId, action)); err != nil {
			return fmt.Errorf("could not upsert action: %v", err.Error())
		}
	}

	err = tx.Commit()

	if err != nil {
		return err
	}

	return nil
}

func (c *PostgresConnector) GetTableNamesOnSchema(sch string) ([]string, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		blog.Error("Could not acquire connection to get existing table names", "error", err)
		return []string{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getExistingTablesSql(sch))

	if err != nil {
		blog.Error("Could not query existing table names", "error", err)
		return []string{}, err
	}

	tableNames := []string{}

	for rows.Next() {
		var tableName string
		err := rows.Scan(&tableName)

		if err != nil {
			blog.Error("Could not scan table name", "error", err)
			return []string{}, err
		}

		tableNames = append(tableNames, tableName)
	}

	return tableNames, nil
}

func (c *PostgresConnector) getExistingTriggers() ([]schema.Trigger, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		return []schema.Trigger{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getExistingTriggersSql())

	if err != nil {
		return []schema.Trigger{}, err
	}

	triggerIds := []schema.Trigger{}

	for rows.Next() {
		var rawTriggerName string
		err := rows.Scan(&rawTriggerName)

		if err != nil {
			blog.Error("Could not scan trigger name", "error", err)
			continue
		}

		tg, err := db.UnmarshalTriggerName(rawTriggerName)

		if err != nil {
			blog.Error("Could not parse trigger name", "error", err)
			continue
		}

		triggerIds = append(triggerIds, tg)
	}

	return triggerIds, nil
}

func (c *PostgresConnector) listen() {
	conn, err := c.pool.Acquire()

	defer c.close(conn)

	if err != nil {
		blog.Fatal("Could not acquire connection to LISTEN", "error", err)
		return
	}

	err = conn.Listen(db.BeaconEventName)
	if err != nil {
		blog.Fatal("Could not establish LISTEN connection", "error", err)
		return
	}

	for {
		msg, err := conn.WaitForNotification(c.shutdown.Ctx)
		if err != nil {
			blog.Info("Event listener shutdown")
			return
		}

		events := c.parseEvents(msg.Payload)

		for _, event := range events {
			go c.ps.Publish(event)
		}
	}
}

type PgNotifyPayload struct {
	Schema    string      `json:"schema"`
	Table     string      `json:"table"`
	Operation string      `json:"operation"`
	Old       interface{} `json:"old"`
	New       interface{} `json:"new"`
}

// Memoizes metadata queries for 10 seconds
var memo = util.NewMemo[[]schema.Action](time.Second * 10)

func (c *PostgresConnector) parseEvents(payload string) []db.EventPayload {
	if payload == "" {
		blog.Warn("Received empty payload from database")
		return []db.EventPayload{}
	}

	notifyPayload := PgNotifyPayload{}
	err := json.Unmarshal([]byte(payload), &notifyPayload)

	if err != nil {
		blog.Error("could not unmarshal payload from database", "error", err, "payload", payload)
		return []db.EventPayload{}
	}

	tg := c.triggerFromPgNotifyPayload(notifyPayload)
	memoKey := db.MarshalTriggerName(tg)
	actions, err := memo.Call(memoKey, func() ([]schema.Action, error) {
		return c.getActionsFromTrigger(tg)
	})

	if err != nil {
		blog.Error("could not get actions from trigger", "error", err)
		return []db.EventPayload{}
	}

	events := []db.EventPayload{}

	for _, action := range actions {
		def := schema.Definition{
			Trigger: tg,
			Action:  action,
		}
		event := db.EventPayload{
			Definition: def,
			Old:        notifyPayload.Old,
			New:        notifyPayload.New,
		}
		events = append(events, event)
	}

	return events
}

func (c *PostgresConnector) getActionsFromTrigger(tg schema.Trigger) ([]schema.Action, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		return []schema.Action{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getActionSelectSql(tg))

	if err != nil {
		return []schema.Action{}, err
	}

	metas := []schema.Action{}

	for rows.Next() {
		var metaJson string
		err := rows.Scan(&metaJson)

		if err != nil {
			blog.Error("Could not scan trigger action", "error", err)
			continue
		}

		action := schema.Action{}
		err = json.Unmarshal([]byte(metaJson), &action)
		if err != nil {
			blog.Error("Could not unmarshal trigger action", "error", err)
			continue
		}

		metas = append(metas, action)
	}

	return metas, nil
}

func (c *PostgresConnector) close(conn *pgx.Conn) {
	blog.Info("Closing Postgres listener")
	c.pool.Release(conn)
	c.shutdown.WaitGroup.Done()
}

func (c *PostgresConnector) triggerFromPgNotifyPayload(p PgNotifyPayload) schema.Trigger {
	operation := schema.IOperation(strings.ToLower(p.Operation))

	tg := schema.Trigger{
		Schema:    p.Schema,
		Table:     p.Table,
		Operation: operation,
	}

	return tg
}

// RetryConnector implementation

func (c *PostgresConnector) AddRetry(eventPayload db.EventPayload, response string) error {
	conn, err := c.pool.Acquire()

	if err != nil {
		return err
	}

	defer c.pool.Release(conn)

	eventJson, err := json.Marshal(eventPayload)
	if err != nil {
		return err
	}

	_, err = conn.Exec(fmt.Sprintf(`
	INSERT INTO %[1]s (try_count, event_payload, deferred_at, last_try_at, last_response) VALUES (1, '%[2]s', NOW(), NOW(), '%[3]s');
	`, db.BeaconRetryTableName, eventJson, response))

	if err != nil {
		return err
	}

	return nil
}

func (c *PostgresConnector) GetRetriesTriedLessThan(tryCount int) ([]retry.Retry, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		return []retry.Retry{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(fmt.Sprintf(`
	SELECT id, try_count, event_payload, deferred_at, last_try_at, last_response FROM %[1]s WHERE try_count < %[2]d;
	`, db.BeaconRetryTableName, tryCount))

	if err != nil {
		return []retry.Retry{}, err
	}

	retries := []retry.Retry{}

	for rows.Next() {
		var id int
		var try_count int
		var event_payload string
		var deferred_at time.Time
		var last_try_at time.Time
		var last_response string
		err := rows.Scan(&id, &try_count, &event_payload, &deferred_at, &last_try_at, &last_response)
		if err != nil {
			return []retry.Retry{}, err
		}

		parsedPayload := db.EventPayload{}
		err = json.Unmarshal([]byte(event_payload), &parsedPayload)
		if err != nil {
			return []retry.Retry{}, err
		}

		retry := retry.Retry{
			Id:           id,
			TryCount:     try_count,
			EventPayload: parsedPayload,
			DeferredAt:   deferred_at,
			LastTryAt:    last_try_at,
			LastResponse: last_response,
		}

		retries = append(retries, retry)
	}

	rows.Close()

	if err != nil {
		return []retry.Retry{}, err
	}

	return retries, nil
}

func (c *PostgresConnector) DeleteRetry(id int) error {
	conn, err := c.pool.Acquire()

	if err != nil {
		return err
	}

	defer c.pool.Release(conn)

	_, err = conn.Exec(fmt.Sprintf(`
	DELETE FROM %[1]s WHERE id = %[2]d;
	`, db.BeaconRetryTableName, id))

	if err != nil {
		return err
	}

	return nil
}

func (c *PostgresConnector) BumpRetry(id int, response string) error {
	conn, err := c.pool.Acquire()

	if err != nil {
		return err
	}

	defer c.pool.Release(conn)

	_, err = conn.Exec(fmt.Sprintf(`
	UPDATE %[1]s SET try_count = try_count + 1, last_try_at = NOW(), last_response = '%[2]s' WHERE id = %[3]d;
	`, db.BeaconRetryTableName, response, id))

	if err != nil {
		return err
	}

	return nil
}
