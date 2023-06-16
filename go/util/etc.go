package util

import (
	"errors"
	"sync"
)

func WaitGroupToChan(wg *sync.WaitGroup) chan struct{} {
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}

func Includes[T string](slice []T, target T) bool {
	for _, element := range slice {
		if element == target {
			return true
		}
	}
	return false
}

func MapIncludes[K comparable, T string](values map[K]T, target T) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func FindMapKeyByValue[K ~int, V comparable](values map[K]V, target V) (K, error) {
	for key, value := range values {
		if value == target {
			return key, nil
		}
	}
	return 0, errors.New("Value not found in map")
}
