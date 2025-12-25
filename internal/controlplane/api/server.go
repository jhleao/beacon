// Package api provides the HTTP server for the control plane.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"beacon/internal/config"
	"beacon/internal/controlplane/service"
	"beacon/internal/db"
	"beacon/internal/observability"

	"gopkg.in/yaml.v3"
)

// Server is the control plane HTTP server.
type Server struct {
	pool     *db.Pool
	applySvc *service.ApplyService
	addr     string
	secret   string
	logger   *slog.Logger
	mux      *http.ServeMux
	metrics  *observability.Metrics
}

// NewServer creates a control plane server.
func NewServer(
	pool *db.Pool,
	applySvc *service.ApplyService,
	addr string,
	secret string,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Server {
	s := &Server{
		pool:     pool,
		applySvc: applySvc,
		addr:     addr,
		secret:   secret,
		logger:   logger,
		mux:      http.NewServeMux(),
		metrics:  metrics,
	}

	s.routes()
	return s
}

func (s *Server) routes() {
	// Public endpoints (no auth)
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Protected endpoints
	s.mux.HandleFunc("GET /metrics", s.authRequired(s.handleMetrics))
	s.mux.HandleFunc("POST /apply", s.authRequired(s.handleApply))
	s.mux.HandleFunc("GET /config", s.authRequired(s.handleGetConfig))
	s.mux.HandleFunc("POST /validate", s.authRequired(s.handleValidate))
	s.mux.HandleFunc("GET /destinations", s.authRequired(s.handleListDestinations))
	s.mux.HandleFunc("GET /subscriptions", s.authRequired(s.handleListSubscriptions))
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Start runs the HTTP server (blocks until context cancelled).
func (s *Server) Start(ctx context.Context) error {
	server := &http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("control plane listening", "addr", s.addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("shutting down control plane")
		return server.Shutdown(context.Background())
	}
}

// authRequired wraps a handler with authentication.
func (s *Server) authRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			s.writeError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			s.writeError(w, http.StatusUnauthorized, "invalid authorization format")
			return
		}

		token := strings.TrimPrefix(auth, prefix)
		if token != s.secret {
			s.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	err := s.pool.Ping(ctx)
	if err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, observability.Unhealthy(err))
		return
	}

	s.writeJSON(w, http.StatusOK, observability.Healthy(0))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics != nil {
		s.metrics.Handler().ServeHTTP(w, r)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	cfg, err := config.Parse(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := cfg.Validate(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	dryRun := r.URL.Query().Get("dry_run") == "true"

	var result *service.ApplyResult
	if dryRun {
		result, err = s.applySvc.DryRun(r.Context(), cfg)
	} else {
		result, err = s.applySvc.Apply(r.Context(), cfg)
	}

	if err != nil {
		s.logger.Error("apply failed", "error", err, "dry_run", dryRun)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.logger.Info("configuration applied",
		"dry_run", dryRun,
		"destinations_created", len(result.Destinations.Created),
		"destinations_updated", len(result.Destinations.Updated),
		"destinations_deleted", len(result.Destinations.Deleted),
		"subscriptions_created", len(result.Subscriptions.Created),
		"subscriptions_updated", len(result.Subscriptions.Updated),
		"subscriptions_deleted", len(result.Subscriptions.Deleted),
	)

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.applySvc.Export(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		s.logger.Error("failed to encode config", "error", err)
	}
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	cfg, err := config.Parse(body)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{
			"valid":  false,
			"errors": []string{err.Error()},
		})
		return
	}

	if err := cfg.Validate(); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{
			"valid":  false,
			"errors": []string{err.Error()},
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Server) handleListDestinations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, name, url, method, timeout_ms, max_in_flight, created_at
		FROM beacon.destinations
		ORDER BY name
	`)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var destinations []map[string]any
	for rows.Next() {
		var id, name, url, method string
		var timeoutMs, maxInFlight int
		var createdAt string

		if err := rows.Scan(&id, &name, &url, &method, &timeoutMs, &maxInFlight, &createdAt); err != nil {
			continue
		}

		destinations = append(destinations, map[string]any{
			"id":            id,
			"name":          name,
			"url":           url,
			"method":        method,
			"timeout_ms":    timeoutMs,
			"max_in_flight": maxInFlight,
			"created_at":    createdAt,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"destinations": destinations})
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	includeDraining := r.URL.Query().Get("include_draining") == "true"

	query := `
		SELECT s.id, s.name, s.enabled, s.draining, s.table_schema, s.table_name,
			   s.operation, d.id, d.name, s.trigger_columns, s.payload_columns, s.created_at
		FROM beacon.subscriptions s
		JOIN beacon.destinations d ON d.id = s.destination_id
		WHERE s.deleted_at IS NULL
	`
	if !includeDraining {
		query += " AND s.draining = false"
	}
	query += " ORDER BY s.name"

	rows, err := s.pool.Query(r.Context(), query)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var subscriptions []map[string]any
	for rows.Next() {
		var subID, name, tableSchema, tableName, operation, destID, destName string
		var enabled, draining bool
		var triggerCols, payloadCols []string
		var createdAt string

		if err := rows.Scan(&subID, &name, &enabled, &draining, &tableSchema, &tableName,
			&operation, &destID, &destName, &triggerCols, &payloadCols, &createdAt); err != nil {
			continue
		}

		subscriptions = append(subscriptions, map[string]any{
			"id":               subID,
			"name":             name,
			"enabled":          enabled,
			"draining":         draining,
			"table_schema":     tableSchema,
			"table_name":       tableName,
			"operation":        operation,
			"destination_id":   destID,
			"destination_name": destName,
			"trigger_columns":  triggerCols,
			"payload_columns":  payloadCols,
			"created_at":       createdAt,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subscriptions})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}
