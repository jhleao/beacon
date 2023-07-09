package util

import (
	"time"
)

type Memo[T interface{}] struct {
	cache   map[string]*cacheEntry[T]
	ttl     time.Duration
	cleaner *time.Ticker
}

type cacheEntry[T interface{}] struct {
	value      T
	expiration time.Time
}

func NewMemo[T interface{}](ttl time.Duration) *Memo[T] {
	m := &Memo[T]{
		cache:   make(map[string]*cacheEntry[T]),
		cleaner: time.NewTicker(ttl),
		ttl:     ttl,
	}

	go m.startCleaner()

	return m
}

func (m *Memo[T]) Call(key string, fn func() (T, error)) (T, error) {
	entry, ok := m.cache[key]

	if ok && entry.expiration.After(time.Now()) {
		return entry.value, nil
	}

	if ok {
		delete(m.cache, key)
	}

	value, err := fn()

	if err != nil {
		return value, err
	}

	expiration := time.Now().Add(m.ttl)
	m.cache[key] = &cacheEntry[T]{
		value:      value,
		expiration: expiration,
	}

	return value, nil
}

func (m *Memo[T]) startCleaner() {
	for key, entry := range m.cache {
		if entry.expiration.Before(time.Now()) {
			delete(m.cache, key)
		}
	}
}
