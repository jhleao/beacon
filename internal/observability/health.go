package observability

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status   string            `json:"status"`
	Database string            `json:"database"`
	Workers  int               `json:"workers"`
	Details  map[string]string `json:"details,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// Healthy returns a healthy HealthResponse.
func Healthy(workers int) HealthResponse {
	return HealthResponse{
		Status:   "healthy",
		Database: "connected",
		Workers:  workers,
	}
}

// Unhealthy returns an unhealthy HealthResponse.
func Unhealthy(err error) HealthResponse {
	return HealthResponse{
		Status:   "unhealthy",
		Database: "disconnected",
		Workers:  0,
		Error:    err.Error(),
	}
}
