package main

import (
	"beacon/go/common"
	"beacon/go/db/postgres"
	"beacon/go/handler"
	"beacon/go/log"
	"beacon/go/schema/yml"
	"beacon/go/web"
	"os"
)

func initServices(sh common.Shutdown) {

	pg := postgres.NewPostgresConnector(os.Getenv("POSTGRES_URI"), sh)

	pg.Initialize()

	h := handler.NewEventHandler()
	pg.Subscribe(h.Handle)

	parser := yml.NewYmlSchemaParser(pg)

	server := web.NewBeaconServer(sh, parser, pg, os.Getenv("BEACON_PORT"), os.Getenv("BEACON_ADMIN_TOKEN"))

	server.Start()

	log.Info("Services started")
}

func main() {
	log.Init()
	common.LoadEnv()
	common.WithShutdown(initServices)
}
