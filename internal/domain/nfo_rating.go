package domain

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

const NFOItemRatingFieldsVersion = "multi-source-ratings-v1"
const MaxMetadataRatings = 128
const MaxMetadataRatingNameBytes = 16384

type NFOSourceRating struct {
	Name    string   `json:"name"`
	Value   float64  `json:"value"`
	Max     *float64 `json:"max,omitempty"`
	Votes   *int     `json:"votes,omitempty"`
	Default bool     `json:"default"`
}

func ValidMetadataRatings(ratings []NFOSourceRating) bool {
	if ratings == nil || len(ratings) > MaxMetadataRatings {
		return false
	}
	total := 0
	for _, rating := range ratings {
		if !utf8.ValidString(rating.Name) || len(rating.Name) > 1024 || strings.ContainsRune(rating.Name, 0) {
			return false
		}
		total += len(rating.Name)
		if total > MaxMetadataRatingNameBytes {
			return false
		}
		maximum := 10.0
		if rating.Max != nil {
			if math.IsNaN(*rating.Max) || math.IsInf(*rating.Max, 0) || *rating.Max <= 0 || *rating.Max > 1000000 {
				return false
			}
			maximum = *rating.Max
		}
		if math.IsNaN(rating.Value) || math.IsInf(rating.Value, 0) || rating.Value < 0 || rating.Value > maximum {
			return false
		}
		if rating.Votes != nil && (*rating.Votes < 0 || *rating.Votes > 2147483647) {
			return false
		}
	}
	return true
}

func CloneNFORatings(ratings []NFOSourceRating) []NFOSourceRating {
	result := slices.Clone(ratings)
	for i := range result {
		if result[i].Max != nil {
			v := *result[i].Max
			result[i].Max = &v
		}
		if result[i].Votes != nil {
			v := *result[i].Votes
			result[i].Votes = &v
		}
	}
	return result
}

func EqualNFORatings(a, b []NFOSourceRating) bool {
	return slices.EqualFunc(a, b, func(a, b NFOSourceRating) bool {
		return a.Name == b.Name && a.Value == b.Value && a.Default == b.Default && (a.Max == nil && b.Max == nil || a.Max != nil && b.Max != nil && *a.Max == *b.Max) && (a.Votes == nil && b.Votes == nil || a.Votes != nil && b.Votes != nil && *a.Votes == *b.Votes)
	})
}

func ValidMetadataRatingValue(raw json.RawMessage) bool {
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return false
	}
	if string(raw) == "null" {
		return true
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return false
	}
	ratings := make([]NFOSourceRating, 0, len(entries))
	for _, entry := range entries {
		for key := range entry {
			if key != "name" && key != "value" && key != "max" && key != "votes" && key != "default" {
				return false
			}
		}
		var value *float64
		if json.Unmarshal(entry["value"], &value) != nil || value == nil {
			return false
		}
		rating := NFOSourceRating{Value: *value}
		if raw, ok := entry["name"]; ok {
			var name *string
			if json.Unmarshal(raw, &name) != nil || name == nil {
				return false
			}
			rating.Name = *name
		}
		if raw, ok := entry["max"]; ok {
			if json.Unmarshal(raw, &rating.Max) != nil {
				return false
			}
		}
		if raw, ok := entry["votes"]; ok {
			if json.Unmarshal(raw, &rating.Votes) != nil {
				return false
			}
		}
		if raw, ok := entry["default"]; ok {
			var flag *bool
			if json.Unmarshal(raw, &flag) != nil || flag == nil {
				return false
			}
			rating.Default = *flag
		}
		ratings = append(ratings, rating)
	}
	return ValidMetadataRatings(ratings)
}
