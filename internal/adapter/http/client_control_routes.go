package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// clientEndpoint is an administrator account endpoint of the client control
// API. The routes are part of the account rollout; a server built without
// the gate (only in tests) answers them as unavailable.
func (s *Server) clientEndpoint(listQuery bool, operation func(http.ResponseWriter, *http.Request, domain.Actor, *app.ClientControl) (any, int, error)) http.HandlerFunc {
	return s.accountEndpoint(true, listQuery, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if s.clients == nil {
			return nil, 0, domain.ErrDatabase
		}
		return operation(w, r, a, s.clients.service)
	})
}

// clientControlRoutes registers the administrator client control API
// (G47.3, G47.5, G47.8): policy, rules and their observe/enforce switch, hit
// records, statistics and known clients. Every change is audited and takes
// effect on the next request.
func (s *Server) clientControlRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		const base = "/api/v1/client-control"
		r.Get(base+"/policy", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			p, err := svc.Policy(r.Context(), a)
			return p, 200, err
		}))
		r.Put(base+"/policy", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			var input struct {
				UnknownClients *string `json:"unknownClients"`
				ExemptAdmins   *bool   `json:"exemptAdmins"`
				ExemptLoopback *bool   `json:"exemptLoopback"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			if input.UnknownClients == nil || input.ExemptAdmins == nil || input.ExemptLoopback == nil {
				return nil, 0, domain.ErrInvalid
			}
			p, err := svc.SetPolicy(r.Context(), a, domain.ClientPolicy{UnknownClients: *input.UnknownClients, ExemptAdmins: *input.ExemptAdmins, ExemptLoopback: *input.ExemptLoopback})
			return p, 200, err
		}))
		r.Get(base+"/rules", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			rules, err := svc.Rules(r.Context(), a)
			return rules, 200, err
		}))
		r.Post(base+"/rules", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			in, err := decodeClientRule(w, r)
			if err != nil {
				return nil, 0, err
			}
			rule, err := svc.CreateRule(r.Context(), a, in)
			return rule, 201, err
		}))
		r.Get(base+"/rules/{id}", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			rule, err := svc.Rule(r.Context(), a, chi.URLParam(r, "id"))
			return rule, 200, err
		}))
		r.Put(base+"/rules/{id}", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			in, err := decodeClientRule(w, r)
			if err != nil {
				return nil, 0, err
			}
			rule, err := svc.UpdateRule(r.Context(), a, chi.URLParam(r, "id"), in)
			return rule, 200, err
		}))
		r.Delete(base+"/rules/{id}", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, svc.DeleteRule(r.Context(), a, chi.URLParam(r, "id"))
		}))
		for _, mode := range []struct {
			path    string
			enforce bool
		}{{"/enforce", true}, {"/observe", false}} {
			r.Post(base+"/rules/{id}"+mode.path, s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
				if err := emptyAccountInput(w, r); err != nil {
					return nil, 0, err
				}
				rule, err := svc.SetRuleEnforcing(r.Context(), a, chi.URLParam(r, "id"), mode.enforce)
				return rule, 200, err
			}))
		}
		r.Get(base+"/hits", s.clientEndpoint(true, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			query, err := strictQuery(r, "ruleId", "mode", "since", "until", "cursor", "limit")
			if err != nil {
				return nil, 0, err
			}
			filter, err := clientHitFilter(query)
			if err != nil {
				return nil, 0, err
			}
			limit, err := queryLimit(query, 50)
			if err != nil {
				return nil, 0, err
			}
			s.flushClientRecords(r.Context())
			hits, next, err := svc.Hits(r.Context(), a, filter, query["cursor"], limit)
			if err != nil {
				return nil, 0, err
			}
			return map[string]any{"hits": hits, "pagination": map[string]any{"nextCursor": next, "limit": limit}}, 200, nil
		}))
		r.Get(base+"/hits/export", s.clientEndpoint(true, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			query, err := strictQuery(r, "ruleId", "mode", "since", "until")
			if err != nil {
				return nil, 0, err
			}
			filter, err := clientHitFilter(query)
			if err != nil {
				return nil, 0, err
			}
			s.flushClientRecords(r.Context())
			hits, err := svc.ExportHits(r.Context(), a, filter)
			if err != nil {
				return nil, 0, err
			}
			w.Header().Set("Content-Disposition", `attachment; filename="client-control-hits.json"`)
			return map[string]any{"hits": hits, "count": len(hits)}, 200, nil
		}))
		r.Get(base+"/stats", s.clientEndpoint(true, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			query, err := strictQuery(r, "hours", "top")
			if err != nil {
				return nil, 0, err
			}
			hours, top := 24, 10
			if v, ok := query["hours"]; ok {
				if hours, err = strconv.Atoi(v); err != nil {
					return nil, 0, domain.ErrInvalid
				}
			}
			if v, ok := query["top"]; ok {
				if top, err = strconv.Atoi(v); err != nil {
					return nil, 0, domain.ErrInvalid
				}
			}
			if hours < 1 || hours > 720 {
				return nil, 0, domain.ErrInvalid
			}
			s.flushClientRecords(r.Context())
			stats, err := svc.Stats(r.Context(), a, time.Duration(hours)*time.Hour, top, time.Now())
			return stats, 200, err
		}))
		r.Get(base+"/clients", s.clientEndpoint(true, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			query, err := strictQuery(r, "cursor", "limit")
			if err != nil {
				return nil, 0, err
			}
			limit, err := queryLimit(query, 50)
			if err != nil {
				return nil, 0, err
			}
			s.flushClientRecords(r.Context())
			clients, next, err := svc.Clients(r.Context(), a, query["cursor"], limit)
			if err != nil {
				return nil, 0, err
			}
			return map[string]any{"clients": clients, "pagination": map[string]any{"nextCursor": next, "limit": limit}}, 200, nil
		}))
		r.Patch(base+"/clients/{id}", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			var input domain.KnownClientUpdate
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			client, err := svc.UpdateClient(r.Context(), a, chi.URLParam(r, "id"), input)
			return client, 200, err
		}))
		r.Post(base+"/clients/{id}/block", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			rule, err := svc.BlockClient(r.Context(), a, chi.URLParam(r, "id"))
			return rule, 201, err
		}))
		r.Post(base+"/clients/{id}/kick", s.clientEndpoint(false, func(w http.ResponseWriter, r *http.Request, a domain.Actor, svc *app.ClientControl) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			n, err := svc.KickClient(r.Context(), a, chi.URLParam(r, "id"))
			return map[string]int64{"sessionsRevoked": n}, 200, err
		}))
	})
}

// clientRuleBody is the wire form of a rule; enabled is required so an
// omitted member never silently disables a rule.
type clientRuleBody struct {
	Dimension   string                   `json:"dimension"`
	Header      string                   `json:"header"`
	Match       string                   `json:"match"`
	Pattern     string                   `json:"pattern"`
	CaseFold    bool                     `json:"caseFold"`
	Priority    int                      `json:"priority"`
	Action      string                   `json:"action"`
	Intent      string                   `json:"intent"`
	RateLimit   *domain.ClientRuleRate   `json:"rateLimit"`
	ScopeKind   string                   `json:"scopeKind"`
	ScopeValues []string                 `json:"scopeValues"`
	Window      *domain.ClientRuleWindow `json:"window"`
	Enabled     *bool                    `json:"enabled"`
	Note        string                   `json:"note"`
}

func decodeClientRule(w http.ResponseWriter, r *http.Request) (domain.ClientRuleInput, error) {
	var b clientRuleBody
	if err := DecodeJSON(w, r, &b, accountBodyLimit); err != nil {
		return domain.ClientRuleInput{}, err
	}
	if b.Enabled == nil {
		return domain.ClientRuleInput{}, domain.ErrInvalid
	}
	return domain.ClientRuleInput{Dimension: b.Dimension, Header: b.Header, Match: b.Match, Pattern: b.Pattern, CaseFold: b.CaseFold, Priority: b.Priority,
		Action: b.Action, Intent: b.Intent, RateLimit: b.RateLimit, ScopeKind: b.ScopeKind, ScopeValues: b.ScopeValues, Window: b.Window, Enabled: *b.Enabled, Note: b.Note}, nil
}

func clientHitFilter(query map[string]string) (domain.ClientHitFilter, error) {
	f := domain.ClientHitFilter{RuleID: query["ruleId"], Mode: query["mode"]}
	for key, target := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v, ok := query[key]; ok {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, domain.ErrInvalid
			}
			*target = t
		}
	}
	if !f.Valid() {
		return f, domain.ErrInvalid
	}
	return f, nil
}

func queryLimit(query map[string]string, fallback int) (int, error) {
	v, ok := query["limit"]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 100 {
		return 0, domain.ErrInvalid
	}
	return n, nil
}

// flushClientRecords writes this instance's buffered hits and activity
// before an administrator reads them, so a listing reflects the requests
// this instance just served. Failures only delay them to the next flush.
func (s *Server) flushClientRecords(ctx context.Context) {
	if s.clients == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, clientStoreTimeout)
	defer cancel()
	_ = s.clients.Flush(ctx)
}
