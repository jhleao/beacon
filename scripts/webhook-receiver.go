//go:build ignore

// webhook-receiver is a simple HTTP server for testing webhook delivery.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	failRate, _ := strconv.Atoi(os.Getenv("FAIL_RATE"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Log request
		log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		log.Printf("  Headers:")
		for k, v := range r.Header {
			if len(k) > 6 && k[:6] == "Beacon" {
				log.Printf("    %s: %s", k, v[0])
			}
		}

		// Pretty print JSON body
		var pretty map[string]any
		if json.Unmarshal(body, &pretty) == nil {
			formatted, _ := json.MarshalIndent(pretty, "  ", "  ")
			log.Printf("  Body:\n  %s", formatted)
		} else {
			log.Printf("  Body: %s", body)
		}

		// Simulate failures
		if failRate > 0 && rand.Intn(100) < failRate {
			status := []int{500, 502, 503, 504}[rand.Intn(4)]
			log.Printf("  -> Simulated failure: %d", status)
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error": "simulated failure"}`)
			return
		}

		// Success
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"received": true, "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
		log.Printf("  -> 200 OK")
	})

	// Slow endpoint for timeout testing
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := 10 * time.Second
		if d := r.URL.Query().Get("delay"); d != "" {
			if parsed, err := time.ParseDuration(d); err == nil {
				delay = parsed
			}
		}
		log.Printf("[%s] /slow - sleeping %s", r.Method, delay)
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"delayed": true}`)
	})

	// Always fail endpoint
	http.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] /fail - returning 500", r.Method)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": "always fails"}`)
	})

	// 4xx endpoint
	http.HandleFunc("/bad-request", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] /bad-request - returning 400", r.Method)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "bad request"}`)
	})

	log.Printf("Webhook receiver listening on :%s", port)
	log.Printf("Endpoints:")
	log.Printf("  /           - Echo (FAIL_RATE=%d%%)", failRate)
	log.Printf("  /slow?delay=10s - Delayed response")
	log.Printf("  /fail       - Always 500")
	log.Printf("  /bad-request - Always 400")
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
