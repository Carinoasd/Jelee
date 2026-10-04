package httpapi

import (
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Option configures optional server modules.
type Option func(*Server)

// WithWebhooks mounts the G12 webhook administration API. It is required
// when webhooks are enabled.
func WithWebhooks(w *app.Webhooks) Option { return func(s *Server) { s.webhooks = w } }

const webhookBodyLimit = 64 << 10

// webhookInput is the create and replace body. Omitted members take their
// defaults; on replace an omitted headers member keeps the stored headers.
type webhookInput struct {
	Name           string                    `json:"name"`
	URL            string                    `json:"url"`
	Enabled        *bool                     `json:"enabled"`
	Events         []domain.WebhookEventType `json:"events"`
	Headers        map[string]string         `json:"headers"`
	TimeoutSeconds int                       `json:"timeoutSeconds"`
	Retry          *app.WebhookRetryView     `json:"retry"`
}

func (in webhookInput) app() app.WebhookEndpointInput {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return app.WebhookEndpointInput{Name: in.Name, URL: in.URL, Enabled: enabled, Events: in.Events, Headers: in.Headers, TimeoutSeconds: in.TimeoutSeconds, Retry: in.Retry}
}

func (s *Server) webhookRoutes(router chi.Router) {
	router.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		r.Get("/api/v1/webhooks", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			views, err := s.webhooks.List(r.Context(), a)
			return map[string]any{"webhooks": views, "events": domain.WebhookEventTypes()}, 200, err
		}))
		r.Post("/api/v1/webhooks", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var in webhookInput
			if err := DecodeJSON(w, r, &in, webhookBodyLimit); err != nil {
				return nil, 0, err
			}
			view, secret, err := s.webhooks.Create(r.Context(), a, in.app())
			return map[string]any{"webhook": view, "secret": secret}, 201, err
		}))
		r.Get("/api/v1/webhooks/{id}", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			view, err := s.webhooks.Get(r.Context(), a, chi.URLParam(r, "id"))
			return view, 200, err
		}))
		r.Put("/api/v1/webhooks/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var in webhookInput
			if err := DecodeJSON(w, r, &in, webhookBodyLimit); err != nil {
				return nil, 0, err
			}
			view, err := s.webhooks.Update(r.Context(), a, chi.URLParam(r, "id"), in.app())
			return view, 200, err
		}))
		r.Delete("/api/v1/webhooks/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.webhooks.Delete(r.Context(), a, chi.URLParam(r, "id"))
		}))
		r.Post("/api/v1/webhooks/{id}/rotate-secret", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var in struct {
				GraceSeconds *int `json:"graceSeconds"`
			}
			if err := DecodeJSON(w, r, &in, webhookBodyLimit); err != nil {
				return nil, 0, err
			}
			grace := app.DefaultWebhookRotationGrace
			if in.GraceSeconds != nil {
				if *in.GraceSeconds < 0 || *in.GraceSeconds > int(app.MaxWebhookRotationGrace/time.Second) {
					return nil, 0, domain.ErrInvalid
				}
				grace = time.Duration(*in.GraceSeconds) * time.Second
			}
			view, secret, err := s.webhooks.Rotate(r.Context(), a, chi.URLParam(r, "id"), grace)
			return map[string]any{"webhook": view, "secret": secret}, 200, err
		}))
		r.Post("/api/v1/webhooks/{id}/test", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			result, err := s.webhooks.Test(r.Context(), a, chi.URLParam(r, "id"))
			return result, 200, err
		}))
		r.Get("/api/v1/webhooks/{id}/deliveries", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			q, limit, err := jobPageQuery(r, "state")
			if err != nil {
				return nil, 0, err
			}
			rows, err := s.webhooks.Deliveries(r.Context(), a, chi.URLParam(r, "id"), domain.WebhookDeliveryState(q["state"]), q["cursor"], limit)
			if err != nil {
				return nil, 0, err
			}
			last := ""
			if len(rows) > 0 {
				last = rows[len(rows)-1].Cursor
			}
			return pageResult("deliveries", rows, last, len(rows), limit), 200, nil
		}))
		r.Get("/api/v1/webhooks/{id}/deliveries/{deliveryId}", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			detail, err := s.webhooks.Delivery(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "deliveryId"))
			return detail, 200, err
		}))
		r.Post("/api/v1/webhooks/{id}/deliveries/{deliveryId}/replay", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			view, err := s.webhooks.Replay(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "deliveryId"))
			return view, 202, err
		}))
	})
}
