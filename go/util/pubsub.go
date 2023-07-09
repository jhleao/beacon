package util

import (
	"sync"
)

type PubSub[T any] struct {
	subscribers map[int]func(T)
	nextId      int
	mu          sync.Mutex
}

func NewPubSub[T any]() *PubSub[T] {
	return &PubSub[T]{
		subscribers: make(map[int]func(T)),
		nextId:      1,
	}
}

func (ps *PubSub[T]) Subscribe(callback func(T)) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	id := ps.nextId
	ps.subscribers[id] = callback
	ps.nextId++

	return id
}

func (ps *PubSub[T]) Unsubscribe(id int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	delete(ps.subscribers, id)
}

func (ps *PubSub[T]) Publish(value T) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, callback := range ps.subscribers {
		go callback(value)
	}
}

func (ps *PubSub[T]) PublishBlocking(value T) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, callback := range ps.subscribers {
		callback(value)
	}
}
