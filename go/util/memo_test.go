package util

import (
	"testing"
	"time"
)

func TestMemo_Call_CachesValue(t *testing.T) {
	m := NewMemo[string](time.Minute)
	key := "key"
	expected := "cached value"
	fn := func() (string, error) {
		return expected, nil
	}

	result, _ := m.Call(key, fn)

	if result != expected {
		t.Errorf("Expected %v, but got %v", expected, result)
	}
}

func TestMemo_Call_ReturnsCachedValue(t *testing.T) {
	m := NewMemo[string](time.Minute)
	key := "key"
	expected := "cached value"

	m.Call(key, func() (string, error) {
		return expected, nil
	})

	result, _ := m.Call(key, func() (string, error) {
		t.Errorf("Function should not be called when value is cached")
		return "unexpected value", nil
	})

	if result != expected {
		t.Errorf("Expected %v, but got %v", expected, result)
	}
}

func TestMemo_Call_RefreshesCacheAfterTTL(t *testing.T) {
	ttl := time.Millisecond * 100
	m := NewMemo[int64](ttl)
	key := "key"

	fn := func() (int64, error) {
		return time.Now().UnixNano(), nil
	}

	firstResult, _ := m.Call(key, fn)
	time.Sleep(ttl)
	secondResult, _ := m.Call(key, fn)

	if firstResult == secondResult {
		t.Error("Cache should be refreshed after TTL")
	}
}

func TestMemoCleaner(t *testing.T) {
	memo := NewMemo[string](time.Second)

	_, _ = memo.Call("key1", func() (string, error) {
		return "value1", nil
	})

	time.Sleep(2 * time.Second)

	res, _ := memo.Call("key1", func() (string, error) {
		return "value2", nil
	})

	if res != "value2" {
		t.Errorf("Expected cleaner tick to have cleaned up unused cache")
	}
}
