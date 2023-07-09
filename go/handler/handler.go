package handler

import (
	"beacon/go/blog"
	"beacon/go/db"
	"beacon/go/retry"
	"beacon/go/schema"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type EventHandler struct {
	webhookToken string
	retry        retry.Retrier
}

func NewEventHandler(webhookToken string, rt retry.Retrier) *EventHandler {
	return &EventHandler{
		webhookToken: webhookToken,
		retry:        rt,
	}
}

func (h *EventHandler) HandleNewEvent(event db.EventPayload) {
	blog.Info("Received database event", "trigger", event.Definition.Trigger)
	err := h.handle(event)
	if err != nil {
		blog.Info("Error handling event. Scheduling retry")
		err = h.retry.ScheduleRetry(event, err.Error())
		if err != nil {
			blog.Error("Error scheduling retry", "error", err)
		}
	}
}

func (h *EventHandler) HandleRetry(ev retry.RetryEvent) {
	blog.Info("Handling retry", "retryId", ev.Retry.Id, "tryCount", ev.Retry.TryCount)
	err := h.handle(ev.Event)
	if err != nil {
		blog.Info("Retry failed. Bumping", "retryId", ev.Retry.Id, "tryCount", ev.Retry.TryCount)
		err = h.retry.BumpRetry(ev.Retry.Id, err.Error())
		if err != nil {
			blog.Error("Error bumping retry", "retryId", ev.Retry.Id, "tryCount", ev.Retry.TryCount)
		}
	} else {
		blog.Info("Retry succeeded. Cleaning up", "retryId", ev.Retry.Id)
		err = h.retry.DeleteRetry(ev.Retry.Id)
		if err != nil {
			blog.Error("Error deleting retry", "retryId", ev.Retry.Id, "tryCount", ev.Retry.TryCount)
		}
	}

}

func (h *EventHandler) handle(event db.EventPayload) error {
	if event.Definition.Action.Type != schema.ActionType.Http {
		return fmt.Errorf("action type not implemented: %s", event.Definition.Action.Type)
	}

	body := schema.NotificationBody{
		Trigger: event.Definition.Trigger,
		Old:     event.Old,
		New:     event.New,
	}

	json, err := json.Marshal(body)

	if err != nil {
		return fmt.Errorf("error marshalling NotificationBody: %s", err)
	}

	mtd := strings.ToUpper(string(event.Definition.Action.Method))

	req, err := http.NewRequest(mtd, event.Definition.Action.Url, bytes.NewBuffer(json))

	if err != nil {
		return fmt.Errorf("error creating HTTP request: %s", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", h.webhookToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	return nil
}
