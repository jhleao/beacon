package web

import (
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/log"
	"beacon/go/schema"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

type errorResponse struct {
	Message string `json:"message"`
}

type BeaconServer struct {
	adminToken string
	port       int
	sh         common.Shutdown
	schema     schema.Parser
	db         db.Connector
}

func NewBeaconServer(sh common.Shutdown, schemaHandler schema.Parser, db db.Connector, strPort string, adminToken string) *BeaconServer {
	port, err := strconv.Atoi(strPort)

	if err != nil {
		log.Fatal("Invalid port value variable: " + strPort)
	}

	if adminToken == "" {
		log.Fatal("Admin token cannot be empty")
	}

	return &BeaconServer{
		adminToken: adminToken,
		schema:     schemaHandler,
		sh:         sh,
		db:         db,
		port:       port,
	}
}

func (s BeaconServer) Start() {
	s.setupRoutes()

	portStr := fmt.Sprintf(":%d", s.port)

	log.Info("Starting server on port " + portStr)

	go func() {
		if err := http.ListenAndServe(portStr, nil); err != nil {
			log.Fatal(err.Error())
		}
	}()

	log.Info("Server started")
}

func (s BeaconServer) writeErr(w http.ResponseWriter, msg string, status int) {
	responseJSON, err := json.Marshal(errorResponse{Message: msg})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(status)
	w.Write(responseJSON)
}

func (s BeaconServer) handleSchemaPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	log.Info("Received schema patch")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeErr(w, "Failed to read request body", http.StatusBadRequest)
		log.Warn("Failed to read request body", "err", err)
		return
	}

	token := r.Header.Get("X-Beacon-Admin-Token")
	if token != s.adminToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	parsedSchema, err := s.schema.ValidateAndParse(body)
	if err != nil {
		log.Error("Failed to validate schema", "err", err)
		s.writeErr(w, "Failed to validate schema", http.StatusBadRequest)
		return
	}

	err = s.db.ApplySchema(parsedSchema)
	if err != nil {
		s.writeErr(w, "Failed to apply schema", http.StatusInternalServerError)
		log.Error("Failed to apply schema", "err", err)
		return
	}

	log.Info("Schema patch processed successfully")
	w.WriteHeader(http.StatusOK)
}
func (s BeaconServer) setupRoutes() {
	http.HandleFunc("/schema", s.handleSchemaPatch)
}
