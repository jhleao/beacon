package util

import (
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

func Includes[T comparable](slice []T, target T) bool {
	for _, element := range slice {
		if element == target {
			return true
		}
	}
	return false
}
