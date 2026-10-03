package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func setupAt(step SetupStep) SetupState {
	state := NewSetupState()
	state.Current = step
	return state
}

func TestSetupAdvanceTransitionMatrix(t *testing.T) {
	for current := SetupStepLanguage; current <= SetupStepComplete; current++ {
		for step := SetupStep(0); step <= SetupStepComplete+1; step++ {
			next, err := SetupAdvance(setupAt(current), step)
			switch {
			case !step.Valid() || step == SetupStepComplete:
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("advance %s at %s: err=%v, want ErrInvalid", step, current, err)
				}
			case step != current:
				if !errors.Is(err, ErrSetupStepOrder) || next.Current != current {
					t.Fatalf("advance %s at %s: err=%v current=%s", step, current, err, next.Current)
				}
			default:
				if err != nil || next.Current != current+1 {
					t.Fatalf("advance %s: err=%v current=%s", step, err, next.Current)
				}
			}
			if got := SetupCanSubmit(setupAt(current), step); got != (step == current && step != SetupStepComplete) {
				t.Fatalf("SetupCanSubmit(%s,%s)=%v", current, step, got)
			}
		}
	}
}

func TestSetupBackKeepsDataAndStopsAtFirstStep(t *testing.T) {
	state := setupAt(SetupStepMedia)
	state.Locale = "ja-JP"
	state.Admin = SetupAdmin{UserID: "fdf8be29-41af-4b60-ab36-e2edcc31774d", Name: "admin"}
	for want := SetupStepDatabase; want >= SetupStepLanguage; want-- {
		var err error
		if state, err = SetupBack(state); err != nil || state.Current != want {
			t.Fatalf("back: err=%v current=%s want %s", err, state.Current, want)
		}
	}
	if _, err := SetupBack(state); !errors.Is(err, ErrSetupStepOrder) {
		t.Fatalf("back from first step: %v", err)
	}
	if state.Locale != "ja-JP" || state.Admin.Name != "admin" {
		t.Fatalf("back discarded data: %+v", state)
	}
	if !SetupRequiresAdminSession(state) {
		t.Fatal("wizard with a created admin must require that admin")
	}
}

func completeReady() SetupState {
	state := setupAt(SetupStepComplete)
	state.Locale = "zh-CN"
	state.Admin = SetupAdmin{UserID: "fdf8be29-41af-4b60-ab36-e2edcc31774d", Name: "admin"}
	state.Database.SchemaVersion = 4
	state.MetadataPolicy = SetupMetadataPolicy{NFORead: NFOModeReadOnly, NFOWrite: SetupNFOWriteOff}
	state.Network = SetupNetwork{Mode: SetupNetworkLocal, Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}}
	return state
}

func TestSetupFinishAndCompletedRejectsEverything(t *testing.T) {
	now := time.Date(2026, 10, 4, 1, 2, 3, 0, time.FixedZone("x", 3600))
	for step := SetupStepLanguage; step < SetupStepComplete; step++ {
		if _, err := SetupFinish(setupAt(step), now); !errors.Is(err, ErrSetupStepOrder) {
			t.Fatalf("finish at %s: %v", step, err)
		}
	}
	broken := completeReady()
	broken.Admin.UserID = ""
	if _, err := SetupFinish(broken, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finish without admin: %v", err)
	}
	done, err := SetupFinish(completeReady(), now)
	if err != nil || !done.Completed() || !done.CompletedAt.Equal(now) || done.CompletedAt.Location() != time.UTC {
		t.Fatalf("finish: %v %+v", err, done.CompletedAt)
	}
	if _, err := SetupFinish(done, now); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("second finish: %v", err)
	}
	if _, err := SetupBack(done); !errors.Is(err, ErrSetupCompleted) {
		t.Fatalf("back after completion: %v", err)
	}
	for step := SetupStepLanguage; step <= SetupStepComplete; step++ {
		done.Current = step
		if _, err := SetupAdvance(done, step); !errors.Is(err, ErrSetupCompleted) {
			t.Fatalf("advance %s after completion: %v", step, err)
		}
		if SetupCanSubmit(done, step) {
			t.Fatalf("completed state accepts %s", step)
		}
	}
	if SetupRequiresAdminSession(done) {
		t.Fatal("completed wizard has no wizard session rule")
	}
}

func TestEffectiveSetupStatus(t *testing.T) {
	fresh := EffectiveSetupStatus(SetupState{}, false, false)
	if fresh.Completed || fresh.Adopted || fresh.State.Current != SetupStepLanguage {
		t.Fatalf("fresh install: %+v", fresh)
	}
	legacy := EffectiveSetupStatus(SetupState{}, false, true)
	if !legacy.Completed || !legacy.Adopted {
		t.Fatalf("existing install with admin must be completed: %+v", legacy)
	}
	// An admin created by the in-progress wizard does not complete it.
	inProgress := setupAt(SetupStepDatabase)
	inProgress.Admin.UserID = "fdf8be29-41af-4b60-ab36-e2edcc31774d"
	if status := EffectiveSetupStatus(inProgress, true, true); status.Completed || status.Adopted {
		t.Fatalf("in-progress wizard: %+v", status)
	}
	done, _ := SetupFinish(completeReady(), time.Now())
	if status := EffectiveSetupStatus(done, true, false); !status.Completed || status.Adopted {
		t.Fatalf("completed wizard: %+v", status)
	}
}

func TestSetupStepText(t *testing.T) {
	for step := SetupStepLanguage; step <= SetupStepComplete; step++ {
		text, err := step.MarshalText()
		var back SetupStep
		if err != nil || back.UnmarshalText(text) != nil || back != step {
			t.Fatalf("round trip %d: %s %v", step, text, err)
		}
	}
	var step SetupStep
	if step.UnmarshalText([]byte("finish")) == nil {
		t.Fatal("unknown step accepted")
	}
	if _, err := SetupStep(0).MarshalText(); err == nil {
		t.Fatal("zero step marshalled")
	}
}

// The persisted state must not be able to carry secrets.
func TestSetupStateHasNoSecretFields(t *testing.T) {
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeFor[time.Time]() {
			return
		}
		for field := range typ.Fields() {
			name := strings.ToLower(field.Name)
			for _, banned := range []string{"password", "hash", "secret", "token", "key", "credential"} {
				if strings.Contains(name, banned) {
					t.Fatalf("setup state field %s.%s may hold a secret", path, field.Name)
				}
			}
			walk(field.Type, path+"."+field.Name)
		}
	}
	walk(reflect.TypeFor[SetupState](), "SetupState")
	encoded, err := json.Marshal(completeReady())
	if err != nil || !strings.Contains(string(encoded), `"current":"complete"`) {
		t.Fatalf("encode: %s %v", encoded, err)
	}
}

func TestSetupGateFor(t *testing.T) {
	cases := []struct {
		completed    bool
		method, path string
		want         SetupGate
	}{
		{false, "GET", "/healthz", SetupGateAllow},
		{false, "GET", "/readyz", SetupGateAllow},
		{false, "GET", "/api/v1/system", SetupGateAllow},
		{false, "GET", "/api/v1/openapi.json", SetupGateAllow},
		{false, "GET", "/api-docs", SetupGateAllow},
		{false, "GET", "/api/v1/setup", SetupGateAllow},
		{false, "POST", "/api/v1/setup/steps/admin", SetupGateAllow},
		{false, "GET", "/api/v1/items", SetupGateRequired},
		{false, "POST", "/api/v1/auth/login", SetupGateRequired},
		{false, "GET", "/api/v1/users", SetupGateRequired},
		{false, "GET", "/Users/Me", SetupGateRequired},
		{false, "GET", "/api/v1/setupx", SetupGateRequired},
		{false, "GET", "/api/v1/setup/../users", SetupGateRequired},
		{false, "GET", "/api/v1/setup/..", SetupGateRequired},
		{false, "GET", "//api/v1/setup", SetupGateRequired},
		{false, "GET", "/api/v1/setup/%2e%2e/users", SetupGateRequired},
		{false, "GET", "", SetupGateRequired},
		{false, "GET", "/healthz/", SetupGateRequired},
		{true, "GET", "/api/v1/items", SetupGateAllow},
		{true, "POST", "/api/v1/auth/login", SetupGateAllow},
		{true, "GET", "/api/v1/setup", SetupGateAllow},
		{true, "HEAD", "/api/v1/setup", SetupGateAllow},
		{true, "POST", "/api/v1/setup/steps/admin", SetupGateCompleted},
		{true, "PUT", "/api/v1/setup", SetupGateCompleted},
	}
	for _, c := range cases {
		if got := SetupGateFor(c.completed, c.method, c.path); got != c.want {
			t.Errorf("SetupGateFor(%v,%s,%q)=%d want %d", c.completed, c.method, c.path, got, c.want)
		}
	}
}
