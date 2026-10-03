package config

import "testing"

func TestFamilyIgnoreExplicitRollout(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	c, err := LoadWith(lookup)
	if err != nil || c.EnableFamilyIgnore {
		t.Fatal("family default enabled")
	}
	values["JELEE_ENABLE_FAMILY_IGNORE"] = "invalid"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("invalid flag accepted")
	}
	values["JELEE_ENABLE_FAMILY_IGNORE"] = "true"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("family without jobs accepted")
	}
	values["JELEE_ENABLE_JOBS"] = "true"
	if _, err = LoadWith(lookup); err == nil {
		t.Fatal("family jobs without accounts accepted")
	}
	values["JELEE_ENABLE_ACCOUNTS"] = "true"
	c, err = LoadWith(lookup)
	if err != nil || !c.EnableFamilyIgnore {
		t.Fatal("valid family rollout rejected", err)
	}
}
