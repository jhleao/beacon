//go:build ignore

// webhook-receiver is a simple HTTP server for testing webhook delivery.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	failRate, _ := strconv.Atoi(os.Getenv("FAIL_RATE"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Collect Beacon headers
		beaconHeaders := make(map[string]string)
		for k, v := range r.Header {
			if len(k) > 6 && k[:6] == "Beacon" {
				beaconHeaders[k] = v[0]
			}
		}

		// Pretty print JSON body
		var bodyStr string
		var pretty map[string]any
		if json.Unmarshal(body, &pretty) == nil {
			formatted, _ := json.MarshalIndent(pretty, "", "  ")
			bodyStr = string(formatted)
		} else {
			bodyStr = string(body)
		}

		logger.Info("request received",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"headers", beaconHeaders,
			"body", bodyStr,
		)

		// Simulate failures
		if failRate > 0 && rand.Intn(100) < failRate {
			status := []int{500, 502, 503, 504}[rand.Intn(4)]
			logger.Warn("simulated failure", "status", status)
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error": "simulated failure"}`)
			return
		}

		// Success
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"received": true, "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
		logger.Debug("response sent", "status", 200)
	})

	// Slow endpoint for timeout testing
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := 10 * time.Second
		if d := r.URL.Query().Get("delay"); d != "" {
			if parsed, err := time.ParseDuration(d); err == nil {
				delay = parsed
			}
		}
		logger.Info("slow request", "method", r.Method, "delay", delay)
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"delayed": true}`)
	})

	// Always fail endpoint
	http.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("fail endpoint hit", "method", r.Method)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": "always fails"}`)
	})

	// 4xx endpoint
	http.HandleFunc("/bad-request", func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("bad-request endpoint hit", "method", r.Method)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "bad request"}`)
	})

	logger.Info("webhook receiver starting",
		"port", port,
		"fail_rate", failRate,
	)
	logger.Info("endpoints",
		"echo", "/",
		"slow", "/slow?delay=10s",
		"fail", "/fail",
		"bad_request", "/bad-request",
	)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
