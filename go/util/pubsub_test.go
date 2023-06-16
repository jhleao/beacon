package util

import (
	"sync"
	"testing"
)

func TestPubSub_Subscribe(t *testing.T) {
	ps := NewPubSub[int]()
	callback := func(value int) {}

	subscriberID := ps.Subscribe(callback)

	if subscriberID <= 0 {
		t.Errorf("Subscribe() did not return a valid subscriber ID")
	}

	if ps.subscribers[subscriberID] == nil {
		t.Errorf("Subscribe() did not add the callback function to the subscribers map")
	}
}

func TestPubSub_Unsubscribe(t *testing.T) {
	ps := NewPubSub[int]()
	callback := func(value int) {}

	subscriberID := ps.Subscribe(callback)

	ps.Unsubscribe(subscriberID)

	if _, exists := ps.subscribers[subscriberID]; exists {
		t.Errorf("Unsubscribe() did not remove the callback function from the subscribers map")
	}
}

func TestPubSub_Publish(t *testing.T) {
	ps := NewPubSub[int]()
	var wg sync.WaitGroup

	callbackCount := 0
	callback := func(value int) {
		defer wg.Done()
		callbackCount++
	}

	subscriberID1 := ps.Subscribe(callback)
	subscriberID2 := ps.Subscribe(callback)
	subscriberID3 := ps.Subscribe(callback)

	expectedCallbackCount := 3
	wg.Add(expectedCallbackCount)

	ps.Publish(42)

	wg.Wait()

	if callbackCount != expectedCallbackCount {
		t.Errorf("Publish() did not trigger the callback functions the expected number of times")
	}

	ps.Unsubscribe(subscriberID1)
	ps.Unsubscribe(subscriberID2)
	ps.Unsubscribe(subscriberID3)
}

func TestPubSub_MultipleTypes(t *testing.T) {
	ps := NewPubSub[interface{}]()
	callback := func(value interface{}) {}

	subscriberID := ps.Subscribe(callback)

	if subscriberID <= 0 {
		t.Errorf("Subscribe() did not return a valid subscriber ID")
	}

	if ps.subscribers[subscriberID] == nil {
		t.Errorf("Subscribe() did not add the callback function to the subscribers map")
	}

	ps.Publish("Hello, World!")
	ps.Publish(42)
	ps.Unsubscribe(subscriberID)

	if _, exists := ps.subscribers[subscriberID]; exists {
		t.Errorf("Unsubscribe() did not remove the callback function from the subscribers map")
	}
}
