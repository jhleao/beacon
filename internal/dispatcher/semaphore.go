package dispatcher

import (
	"sync"

	"github.com/google/uuid"
)

// Semaphores manages per-destination concurrency limits.
type Semaphores struct {
	mu   sync.RWMutex
	sems map[uuid.UUID]chan struct{}
}

// NewSemaphores creates a new Semaphores manager.
func NewSemaphores() *Semaphores {
	return &Semaphores{
		sems: make(map[uuid.UUID]chan struct{}),
	}
}

// Get returns the semaphore for a destination, creating it if needed.
func (s *Semaphores) Get(destID uuid.UUID, maxInFlight int) chan struct{} {
	s.mu.RLock()
	sem, ok := s.sems[destID]
	s.mu.RUnlock()

	if ok {
		return sem
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if sem, ok = s.sems[destID]; ok {
		return sem
	}

	sem = make(chan struct{}, maxInFlight)
	s.sems[destID] = sem
	return sem
}

// Remove removes a semaphore for a destination.
func (s *Semaphores) Remove(destID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sems, destID)
}
