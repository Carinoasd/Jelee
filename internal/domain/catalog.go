// Package domain defines Jelee values without persistence or transport dependencies.
package domain

import "errors"

var (
	ErrNotFound        = errors.New("resource not found")
	ErrUnauthenticated = errors.New("authentication required")
	ErrInvalid         = errors.New("invalid input")
)

type Item struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
}

// ValidID accepts the canonical UUID representation used at the API boundary.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
