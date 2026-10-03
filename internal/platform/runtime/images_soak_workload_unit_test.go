//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestImagesSoakRotateRequiresRevocationIdentityAndTTL(t *testing.T) {
	for _, mode := range []string{"valid", "old-still-valid", "same-token", "wrong-user", "short-ttl", "wrong-role", "new-denied"} {
		t.Run(mode, func(t *testing.T) {
			old := domain.SessionGrant{Token: "old-private-token", User: domain.User{ID: "11000000-0000-4000-8000-000000000001"}, Session: domain.Session{ID: "22000000-0000-4000-8000-000000000001", ClientKind: "web"}}
			grant := old
			grant.Token = "new-private-token"
			grant.Session.ID = "22000000-0000-4000-8000-000000000002"
			grant.Session.UserID = old.User.ID
			grant.Session.DeviceName = "soak"
			grant.Session.CreatedAt = time.Now().UTC()
			grant.Session.ExpiresAt = grant.Session.CreatedAt.Add(24 * time.Hour)
			switch mode {
			case "same-token":
				grant.Token = old.Token
			case "wrong-user":
				grant.User.ID = "11000000-0000-4000-8000-000000000002"
			case "short-ttl":
				grant.Session.ExpiresAt = grant.Session.CreatedAt.Add(time.Hour)
			case "wrong-role":
				grant.User.Admin = true
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/rotate":
					var body struct {
						DeviceName string `json:"deviceName"`
					}
					if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+old.Token || json.NewDecoder(r.Body).Decode(&body) != nil || body.DeviceName != "soak" {
						w.WriteHeader(400)
						return
					}
					_ = json.NewEncoder(w).Encode(struct {
						Data domain.SessionGrant `json:"data"`
					}{grant})
				case "/api/v1/users/me":
					if r.Header.Get("Authorization") == "Bearer "+old.Token && mode != "old-still-valid" || mode == "new-denied" {
						w.WriteHeader(401)
						return
					}
					_ = json.NewEncoder(w).Encode(struct {
						Data domain.User `json:"data"`
					}{grant.User})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			updated, evidence, err := rotateImagesSoakSession(context.Background(), server.Client(), server.URL, old)
			if mode == "valid" {
				if err != nil || updated.Token != grant.Token || !evidence.Rotated || !evidence.OldRejected || !evidence.NewAccepted {
					t.Fatal("valid rotation rejected")
				}
			} else if err == nil || updated.Token != "" {
				t.Fatal("invalid rotation accepted")
			}
			encoded, _ := json.Marshal(evidence)
			if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), old.User.ID) {
				t.Fatal("private session serialized")
			}
		})
	}
}

func TestImagesSoakScanRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	for _, round := range []int{-1, 288} {
		_, code := runImagesSoakScan(context.Background(), nil, "", "", nil, domain.LibraryRegistration{}, round, 1)
		if code != "soak_scan_input_invalid" {
			t.Fatal("invalid round accepted")
		}
	}
	_, code := runImagesSoakScan(context.Background(), nil, "", "", nil, domain.LibraryRegistration{}, 0, 0)
	if code != "soak_scan_input_invalid" {
		t.Fatal("missing fixture bytes accepted")
	}
}
