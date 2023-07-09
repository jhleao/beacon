package main

import (
	"beacon/go/blog"
	"beacon/go/common"
	"beacon/go/db/postgres"
	"beacon/go/handler"
	"beacon/go/retry"
	"beacon/go/schema/yml"
	"beacon/go/web"
	"os"
	"strconv"
)

func initServices(sh common.Shutdown) {
	postgresUri := os.Getenv("POSTGRES_URI")
	whToken := os.Getenv("BEACON_WEBHOOK_TOKEN")
	adminToken := os.Getenv("BEACON_ADMIN_TOKEN")
	port := os.Getenv("BEACON_PORT")
	retryIntervalStr := os.Getenv("BEACON_RETRY_INTERVAL_SECONDS")
	maxRetriesStr := os.Getenv("BEACON_MAX_RETRIES")

	retryInterval, err := strconv.Atoi(retryIntervalStr)
	if err != nil {
		blog.Info("BEACON_RETRY_INTERVAL_SECONDS not set. Using default (120)")
		retryInterval = 120
	}

	if postgresUri == "" {
		postgresUri = "postgres://postgres@127.0.0.1:5432/postgres"
	}

	maxRetries, err := strconv.Atoi(maxRetriesStr)

	if err != nil {
		blog.Info("BEACON_MAX_RETRIES not set. Using default (10)")
		maxRetries = 10
	}

	pg := postgres.NewPostgresConnector(postgresUri, sh)
	rt := retry.NewRetrier(pg, sh, retryInterval, maxRetries)

	h := handler.NewEventHandler(whToken, rt)

	// Subscribe to incoming and retrying events
	pg.Subscribe(h.HandleNewEvent)
	rt.Subscribe(h.HandleRetry)

	pg.Initialize()
	rt.Initialize()

	parser := yml.NewYmlSchemaParser(pg)

	server := web.NewBeaconServer(sh, parser, pg, port, adminToken)

	server.Start()

	blog.Info("Services started")
}

func main() {
	blog.Init()
	common.LoadEnv()
	common.WithShutdown(initServices)
}
