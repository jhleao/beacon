package handler

import (
	"beacon/go/db"
	"beacon/go/log"
	"beacon/go/schema"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type EventHandler struct{}

func NewEventHandler() *EventHandler {
	return &EventHandler{}
}

func (h *EventHandler) Handle(payload db.EventPayload) {
	log.Info("Handling event: ", "event", payload)
	for _, action := range payload.Actions {
		if action.Type != schema.TriggerTypeNames[schema.Http] {
			log.Warn("Action type not implemented: ", "type", action.Type)
			continue
		}

		json, err := json.Marshal(payload)

		if err != nil {
			log.Error("Error marshalling http payload: ", "error", err)
			continue
		}

		log.Info("Sending http notification: ", "url", action.Config["url"])

		req, err := http.NewRequest("POST", action.Config["url"], bytes.NewBuffer(json))

		if err != nil {
			fmt.Println("Error:", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Println("Error sending HTTP request:", err)
			return
		}

		defer resp.Body.Close()

		// TODO Retry logic goes here
		fmt.Println("Response Status:", resp.Status)
	}
}
