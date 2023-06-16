package postgres

import (
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/log"
	"beacon/go/schema"
	"beacon/go/util"
	"encoding/json"
	"errors"
	"fmt"
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

	log.Info("Connected to Postgres")

	c.shutdown.WaitGroup.Add(1)
	go c.listen()

	return nil
}

func (c *PostgresConnector) Subscribe(cb func(payload db.EventPayload)) int {
	return c.ps.Subscribe(cb)
}

func (c *PostgresConnector) ApplySchema(schema schema.Schema) error {
	existingTriggers, err := c.getExistingTriggers()

	if err != nil {
		return err
	}

	schemaTriggerIds := db.GetTriggersFromSchema(schema)

	triggersToUpsert := schemaTriggerIds
	triggersToDelete := []db.Trigger{}

	for _, existingTrigger := range existingTriggers {
		shouldDelete := true
		for _, schemaTrigger := range schemaTriggerIds {
			if db.CompareTriggerIds(existingTrigger, schemaTrigger) {
				shouldDelete = false
				break
			}
		}
		if shouldDelete {
			triggersToDelete = append(triggersToDelete, existingTrigger)
		}

	}

	conn, err := c.pool.Acquire()

	if err != nil {
		log.Error("Could not acquire connection to apply schema", "error", err)
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

	if _, err = tx.Exec(getGlobalProcedureUpsertSql()); err != nil {
		return fmt.Errorf("could not upsert global procedure: %v", err.Error())
	}

	for _, triggerId := range triggersToDelete {
		if _, err = tx.Exec(getTriggerDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger: %v", err.Error())
		}

		if _, err = tx.Exec(getMetadataDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger metadata: %v", err.Error())
		}
	}

	for _, triggerId := range triggersToUpsert {
		if _, err = tx.Exec(getTriggerUpsertSql(triggerId)); err != nil {
			return fmt.Errorf("could not upsert trigger: %v", err.Error())
		}

		// This just recreates all metadata for the trigger. Can be improve to diffing
		if _, err = tx.Exec(getMetadataDeleteSql(triggerId)); err != nil {
			return fmt.Errorf("could not delete trigger metadata: %v", err.Error())
		}

		// TODO improve this mess
		if triggerMeta, err := db.GetTriggerMetaFromSchema(schema, triggerId); err != nil {
			return fmt.Errorf("could not get trigger metadata from schema: %v", err.Error())
		} else {
			if _, err = tx.Exec(getMetadataInsertSql(triggerId, triggerMeta)); err != nil {
				return fmt.Errorf("could not upsert trigger metadata: %v", err.Error())
			}
		}

	}

	err = tx.Commit()

	if err != nil {
		return err
	}

	return nil
}

func (c *PostgresConnector) GetAllTableNames() ([]string, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		log.Error("Could not acquire connection to get existing table names", "error", err)
		return []string{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getExistingTablesSql())

	if err != nil {
		log.Error("Could not query existing table names", "error", err)
		return []string{}, err
	}

	tableNames := []string{}

	for rows.Next() {
		var tableName string
		err := rows.Scan(&tableName)

		if err != nil {
			log.Error("Could not scan table name", "error", err)
			return []string{}, err
		}

		tableNames = append(tableNames, tableName)
	}

	return tableNames, nil
}

func (c *PostgresConnector) getExistingTriggers() ([]db.Trigger, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		return []db.Trigger{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getExistingTriggersSql())

	if err != nil {
		return []db.Trigger{}, err
	}

	triggerIds := []db.Trigger{}

	for rows.Next() {
		var rawTriggerName string
		err := rows.Scan(&rawTriggerName)

		if err != nil {
			log.Error("Could not scan trigger name", "error", err)
			continue
		}

		tg, err := db.UnmarshalTriggerId(rawTriggerName)

		if err != nil {
			log.Error("Could not parse trigger name", "error", err)
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
		log.Fatal("Could not acquire connection to LISTEN", "error", err)
		return
	}

	err = conn.Listen(db.BeaconEventName)
	if err != nil {
		log.Fatal("Could not establish LISTEN connection", "error", err)
		return
	}

	for {
		msg, err := conn.WaitForNotification(c.shutdown.Ctx)
		if err != nil {
			log.Info("Event listener shutdown")
			return
		}
		c.parseEventAndPublish(msg.Payload)
	}
}

func (c *PostgresConnector) parseEventAndPublish(payload string) {
	if payload == "" {
		log.Warn("Received empty payload from database")
		return
	}

	notifyPayload := db.EventPayload{}
	err := json.Unmarshal([]byte(payload), &notifyPayload)

	if err != nil {
		log.Error("could not unmarshal payload from database", "error", err, "payload", payload)
		return
	}

	tg, err := db.TriggerFromEventPayload(notifyPayload)

	if err != nil {
		log.Error("could not parse trigger from event payload", "error", err, "payload", payload)
		return
	}

	tgMeta, err := c.getTriggerMeta(tg)

	if err != nil {
		log.Error("could not get trigger metadata", "error", err, "trigger", tg)
		return
	}

	notifyPayload.Actions = tgMeta

	log.Debug("Received event, publishing to subscribers",
		"schema", notifyPayload.Schema,
		"table", notifyPayload.Table,
		"operation", notifyPayload.Operation,
	)

	go c.ps.Publish(notifyPayload)
}

func (c *PostgresConnector) getTriggerMeta(tg db.Trigger) ([]db.TriggerMeta, error) {
	conn, err := c.pool.Acquire()

	if err != nil {
		return []db.TriggerMeta{}, err
	}

	defer c.pool.Release(conn)

	rows, err := conn.Query(getMetadataSelectSql(tg))

	if err != nil {
		return []db.TriggerMeta{}, err
	}

	metas := []db.TriggerMeta{}

	for rows.Next() {
		var metaJson string
		err := rows.Scan(&metaJson)

		if err != nil {
			log.Error("Could not scan trigger metadata", "error", err)
			continue
		}

		meta := db.TriggerMeta{}
		err = json.Unmarshal([]byte(metaJson), &meta)
		if err != nil {
			log.Error("Could not unmarshal trigger metadata", "error", err)
			continue
		}

		metas = append(metas, meta)
	}

	return metas, nil
}

func (c *PostgresConnector) close(conn *pgx.Conn) {
	log.Info("Closing Postgres listener")
	c.pool.Release(conn)
	c.shutdown.WaitGroup.Done()
}
