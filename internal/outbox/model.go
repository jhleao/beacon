// Package outbox provides the event model and repository for the transactional outbox pattern.
package outbox

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// State represents the event lifecycle state.
type State string

const (
	StatePending    State = "pending"
	StateDelivering State = "delivering"
	StateDelivered  State = "delivered"
	StateDead       State = "dead"
)

// Event represents an outbox event.
type Event struct {
	ID             uuid.UUID
	SubscriptionID uuid.UUID
	OccurredAt     time.Time
	TableSchema    string
	TableName      string
	Operation      string // "INSERT", "UPDATE", "DELETE"
	PK             json.RawMessage
	OldData        json.RawMessage // nil for INSERT
	NewData        json.RawMessage // nil for DELETE
	Payload        json.RawMessage
	State          State
	VisibleAt      time.Time
	LockedBy       *string
	LockedAt       *time.Time
	Attempts       int
	LastError      *string
	CreatedAt      time.Time
}

// Destination contains delivery target info (joined from subscriptions).
type Destination struct {
	ID          uuid.UUID
	Name        string
	URL         string
	Method      string
	Headers     map[string]string
	TimeoutMs   int
	MaxInFlight int
	SSRFPolicy  json.RawMessage
}

// ClaimedEvent bundles an event with its destination.
type ClaimedEvent struct {
	Event       Event
	Destination Destination
}

// DeliveryAttempt records a single delivery attempt.
type DeliveryAttempt struct {
	EventID         uuid.UUID
	DestinationID   uuid.UUID
	Attempt         int
	StartedAt       time.Time
	FinishedAt      time.Time
	StatusCode      *int
	Error           *string
	ResponseHeaders map[string]string
}
