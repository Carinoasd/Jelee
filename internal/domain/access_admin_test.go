package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAccessClockAndWindowValidation(t *testing.T) {
	for _, c := range []struct {
		in   string
		end  bool
		want int
		ok   bool
	}{{"00:00", false, 0, true}, {"23:59", false, 1439, true}, {"24:00", true, 1440, true}, {"24:00", false, 0, false}, {"24:01", true, 0, false},
		{"12:60", false, 0, false}, {"1:00", false, 0, false}, {"ab:cd", false, 0, false}} {
		if got, ok := ParseAccessClock(c.in, c.end); ok != c.ok || ok && got != c.want {
			t.Errorf("ParseAccessClock(%q,%t) = %d %t", c.in, c.end, got, ok)
		}
	}
	if AccessClock(0) != "00:00" || AccessClock(1440) != "24:00" || AccessClock(605) != "10:05" {
		t.Fatal("AccessClock")
	}
	ceiling, high := 13, 22
	valid := AccessWindow{Weekdays: []int{0, 6}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei", RatingMax: &ceiling}
	if !valid.Valid() || !ValidAccessWindows([]AccessWindow{valid}) {
		t.Fatal("valid window refused")
	}
	for _, w := range []AccessWindow{
		{Start: "21:00", End: "21:00", TimeZone: "UTC"}, {Start: "x", End: "07:00", TimeZone: "UTC"}, {Start: "21:00", End: "07:00", TimeZone: ""},
		{Start: "21:00", End: "07:00", TimeZone: "Local"}, {Start: "21:00", End: "07:00", TimeZone: "No/Such_Zone"}, {Start: "21:00", End: "07:00", TimeZone: "UTC", Weekdays: []int{-1}},
		{Start: "21:00", End: "07:00", TimeZone: "UTC", Weekdays: []int{2, 2}}, {Start: "21:00", End: "07:00", TimeZone: "UTC", RatingMax: &high},
		{Start: "21:00", End: "07:00", TimeZone: strings.Repeat("a", 65)},
	} {
		if w.Valid() {
			t.Errorf("invalid window accepted: %+v", w)
		}
	}
	if ValidAccessWindows(make([]AccessWindow, AccessWindowsMax+1)) {
		t.Fatal("too many windows accepted")
	}
	// Friday 22:30 and Saturday 02:30 in Taipei are inside a Friday night
	// window, Saturday 22:30 is not.
	night := AccessWindow{Weekdays: []int{5}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei"}
	friday := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	if !night.Contains(friday) || !night.Contains(friday.Add(4*time.Hour)) || night.Contains(friday.Add(24*time.Hour)) || night.Contains(friday.Add(-2*time.Hour)) {
		t.Fatal("Contains")
	}
	if (AccessWindow{Start: "09:00", End: "10:00", TimeZone: "UTC"}).Contains(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("end is exclusive")
	}
}

func TestAccessBulkAndTemplateValidation(t *testing.T) {
	u1, u2, lib := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"
	if !ValidAccessGrantOperations([]AccessGrantOperation{{Action: AccessGrantAdd, UserIDs: []string{u1, u2}, LibraryIDs: []string{lib}}, {Action: AccessGrantRemove, UserIDs: []string{u1}, LibraryIDs: []string{lib}}}) {
		t.Fatal("valid operations refused")
	}
	for _, ops := range [][]AccessGrantOperation{nil, {{Action: "grant", UserIDs: []string{u1}, LibraryIDs: []string{lib}}}, {{Action: AccessGrantAdd, LibraryIDs: []string{lib}}},
		{{Action: AccessGrantAdd, UserIDs: []string{u1, u1}, LibraryIDs: []string{lib}}}, {{Action: AccessGrantAdd, UserIDs: []string{"x"}, LibraryIDs: []string{lib}}}} {
		if ValidAccessGrantOperations(ops) {
			t.Errorf("invalid operations accepted: %+v", ops)
		}
	}
	if ValidAccessUserIDs(nil) || !ValidAccessUserIDs([]string{u1}) {
		t.Fatal("ValidAccessUserIDs")
	}
	in := AccessTemplateInput{Name: "Kids", LibraryIDs: []string{lib}, ContentAccess: ContentAccess{BlockedTags: []string{}, BlockedKeywords: []string{"gore"}}}
	if !in.Valid() {
		t.Fatal("valid template refused")
	}
	for _, name := range []string{"", " Kids", strings.Repeat("k", 65), "a\x01"} {
		bad := in
		bad.Name = name
		if bad.Valid() {
			t.Errorf("template name %q accepted", name)
		}
	}
	bad := in
	bad.BlockedKeywords = []string{" "}
	if bad.Valid() {
		t.Fatal("blank keyword accepted")
	}
	if !ValidParentalRatings([]ParentalRating{{Code: "KR-15", Level: 15}}) || ValidParentalRatings([]ParentalRating{{Code: " ", Level: 1}}) || ValidParentalRatings([]ParentalRating{{Code: "X", Level: 22}}) {
		t.Fatal("ValidParentalRatings")
	}
}
