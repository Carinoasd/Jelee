package domain

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const NFOItemActorFieldsVersion = "actor-structure-v1"
const MaxMetadataActors = 128
const MaxMetadataActorBytes = 16384

// Thumb is a stored reference, never an instruction to fetch a URL or open a file.
type NFOActor struct {
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Thumb string `json:"thumb,omitempty"`
	Order *int   `json:"order,omitempty"`
}

func ValidMetadataActors(actors []NFOActor) bool {
	if actors == nil || len(actors) > MaxMetadataActors {
		return false
	}
	total := 0
	for _, actor := range actors {
		if strings.TrimSpace(actor.Name) == "" || actor.Order != nil && (*actor.Order < 0 || *actor.Order > 1000000) {
			return false
		}
		for _, field := range []struct {
			value string
			limit int
		}{{actor.Name, 1024}, {actor.Role, 1024}, {actor.Thumb, 4096}} {
			if !utf8.ValidString(field.value) || len(field.value) > field.limit || strings.ContainsRune(field.value, 0) {
				return false
			}
			total += len(field.value)
		}
		if total > MaxMetadataActorBytes {
			return false
		}
	}
	return true
}

func ValidMetadataActorValue(value json.RawMessage) bool {
	if len(value) > 128<<10 || !utf8.Valid(value) {
		return false
	}
	if string(value) == "null" {
		return true
	}
	var actors []NFOActor
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&actors) != nil || !ValidMetadataActors(actors) {
		return false
	}
	return decoder.Decode(new(any)) == io.EOF
}

func CloneNFOActors(actors []NFOActor) []NFOActor {
	result := slices.Clone(actors)
	for i := range result {
		if result[i].Order != nil {
			order := *result[i].Order
			result[i].Order = &order
		}
	}
	return result
}

func EqualNFOActors(a, b []NFOActor) bool {
	return slices.EqualFunc(a, b, func(a, b NFOActor) bool {
		return a.Name == b.Name && a.Role == b.Role && a.Thumb == b.Thumb && (a.Order == nil && b.Order == nil || a.Order != nil && b.Order != nil && *a.Order == *b.Order)
	})
}
