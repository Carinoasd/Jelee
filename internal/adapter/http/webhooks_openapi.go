package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

func webhookSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	bearer := []any{map[string]any{"bearer": []string{}}}
	var types []string
	for _, t := range domain.WebhookEventTypes() {
		types = append(types, string(t))
	}
	eventType := map[string]any{"type": "string", "enum": types}
	outcome := map[string]any{"type": "string", "enum": []string{"delivered", "http", "timeout", "network", "blocked", "tls", "invalid"},
		"description": "delivered: 2xx. http: another status; 408, 425, 429 and 5xx are retried, other statuses dead-letter at once. timeout and network are retried. blocked (target refused by the SSRF policy or allow list), tls (certificate rejected) and invalid (event or endpoint no longer valid) dead-letter at once."}
	state := map[string]any{"type": "string", "enum": []string{"pending", "delivered", "dead"}}
	schemas["WebhookRetry"] = objectSchema(map[string]any{
		"maxAttempts":      map[string]any{"type": "integer", "minimum": 1, "maximum": domain.MaxWebhookAttempts, "default": 8},
		"baseDelaySeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400, "default": 10, "description": "Delay after the first failed attempt; doubled after each further failure."},
		"maxDelaySeconds":  map[string]any{"type": "integer", "minimum": 1, "maximum": 86400, "default": 3600, "description": "Cap of every delay, including a Retry-After hint. At least baseDelaySeconds."},
		"jitter":           map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.2, "description": "Each delay is scaled by a factor drawn uniformly from [1-jitter, 1+jitter]."},
	}, "maxAttempts", "baseDelaySeconds", "maxDelaySeconds", "jitter")
	schemas["WebhookInput"] = objectSchema(map[string]any{
		"name":    stringSchema(128),
		"url":     map[string]any{"type": "string", "maxLength": 2048, "description": "HTTPS only, without credentials or fragment. The host must be on the configured allow list when one is set, and must resolve to public addresses only: private, loopback, link-local and other special-purpose addresses are refused at every connection (G12.5)."},
		"enabled": map[string]any{"type": "boolean", "default": true},
		"events":  map[string]any{"type": "array", "maxItems": 64, "items": eventType, "description": "Subscribed event types; empty or omitted subscribes to every event."},
		"headers": map[string]any{"type": "object", "maxProperties": 16, "additionalProperties": map[string]any{"type": "string", "maxLength": 1024},
			"description": "Custom request headers. Values are stored sealed and never returned. Reserved names (Content-Type, Host, Cookie, User-Agent, X-Jelee-* and hop-by-hop headers) are refused. On replace, omitting headers keeps the stored ones and {} removes them."},
		"timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 10},
		"retry":          schemaRef("WebhookRetry"),
	}, "name", "url")
	schemas["Webhook"] = objectSchema(map[string]any{
		"id": uuid, "name": stringSchema(128), "url": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"},
		"events":              map[string]any{"type": "array", "items": eventType, "description": "Empty means every event."},
		"headerNames":         map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string"}},
		"timeoutSeconds":      map[string]any{"type": "integer", "minimum": 1, "maximum": 30},
		"retry":               schemaRef("WebhookRetry"),
		"previousSecretUntil": map[string]any{"type": "string", "format": "date-time", "description": "Present while the previous secret still signs deliveries after a rotation."},
		"pending":             count, "dead": count, "createdAt": instant, "updatedAt": instant,
	}, "id", "name", "url", "enabled", "events", "headerNames", "timeoutSeconds", "retry", "pending", "dead", "createdAt", "updatedAt")
	schemas["WebhookWithSecret"] = objectSchema(map[string]any{
		"webhook": schemaRef("Webhook"),
		"secret":  map[string]any{"type": "string", "description": "The signing secret, returned only by create and rotate-secret. The whole string, whsec_ prefix included, is the HMAC-SHA256 key."},
	}, "webhook", "secret")
	deliveryProperties := func() map[string]any {
		return map[string]any{
			"id": uuid, "webhookId": uuid, "eventId": map[string]any{"type": "string", "maxLength": 64}, "eventType": eventType, "occurredAt": instant,
			"state": state, "attempts": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.MaxWebhookAttempts, "description": "Attempts since the last replay."},
			"replays": map[string]any{"type": "integer", "minimum": 0}, "nextAttemptAt": instant, "lastOutcome": outcome,
			"lastStatus": map[string]any{"type": "integer", "minimum": 100, "maximum": 599}, "lastAttemptAt": instant, "createdAt": instant,
		}
	}
	deliveryRequired := []string{"id", "webhookId", "eventId", "eventType", "occurredAt", "state", "attempts", "replays", "createdAt"}
	schemas["WebhookDelivery"] = objectSchema(deliveryProperties(), deliveryRequired...)
	schemas["WebhookAttempt"] = objectSchema(map[string]any{
		"round": map[string]any{"type": "integer", "minimum": 1}, "attempt": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.MaxWebhookAttempts},
		"startedAt": instant, "finishedAt": instant, "outcome": outcome, "statusCode": map[string]any{"type": "integer", "minimum": 100, "maximum": 599}, "nextAttemptAt": instant,
	}, "round", "attempt", "startedAt", "finishedAt", "outcome")
	detail := deliveryProperties()
	detail["history"] = map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("WebhookAttempt"), "description": "Most recent attempts first."}
	schemas["WebhookDeliveryDetail"] = objectSchema(detail, append(deliveryRequired, "history")...)
	schemas["WebhookTestResult"] = objectSchema(map[string]any{
		"eventId": map[string]any{"type": "string"}, "outcome": outcome, "statusCode": map[string]any{"type": "integer", "minimum": 100, "maximum": 599},
		"durationMs": map[string]any{"type": "integer", "minimum": 0},
	}, "eventId", "outcome", "durationMs")

	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	admin := func(summary, description string, path bool, statuses ...string) map[string]any {
		op := operation(summary, statuses...)
		op["security"] = bearer
		op["x-jelee-role"] = "administrator"
		op["description"] = "Administrators only. " + description
		if path {
			op["parameters"] = []any{idParameter()}
		}
		return op
	}
	body := func(op map[string]any, schema map[string]any) {
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
	}
	deliveryParameters := []any{idParameter(), map[string]any{"name": "deliveryId", "in": "path", "required": true, "schema": uuid}}

	list := admin("List webhook endpoints", "At most 64 endpoints, oldest first, with their pending and dead-letter counts, and the event catalogue (G12.1). Secrets and header values are never returned.", false, "200", "400", "401", "403", "408", "503")
	list["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(objectSchema(map[string]any{
		"webhooks": map[string]any{"type": "array", "maxItems": 64, "items": schemaRef("Webhook")},
		"events":   map[string]any{"type": "array", "items": eventType},
	}, "webhooks", "events"))
	create := admin("Create a webhook endpoint", "Generates the endpoint's signing secret and returns it once; it is stored sealed with the server master key and cannot be read again. Records the audit event webhook.created.", false, "201", "400", "401", "403", "408", "409", "413", "415", "503")
	body(create, schemaRef("WebhookInput"))
	create["responses"].(map[string]any)["201"].(map[string]any)["content"] = data(schemaRef("WebhookWithSecret"))
	create["responses"].(map[string]any)["400"] = map[string]any{"description": "invalid_request, or webhook_target_denied when the URL breaks the target policy."}
	create["responses"].(map[string]any)["409"] = map[string]any{"description": "conflict: 64 endpoints are configured."}
	paths["/api/v1/webhooks"] = map[string]any{"get": list, "post": create}

	get := admin("Read a webhook endpoint", "", true, "200", "400", "401", "403", "404", "408", "503")
	get["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("Webhook"))
	replace := admin("Replace a webhook endpoint", "Replaces the configuration, including enabled. A disabled endpoint receives no new events and its pending deliveries wait until it is enabled again. Records webhook.updated with the URL host only.", true, "200", "400", "401", "403", "404", "408", "413", "415", "503")
	body(replace, schemaRef("WebhookInput"))
	replace["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("Webhook"))
	replace["responses"].(map[string]any)["400"] = map[string]any{"description": "invalid_request, or webhook_target_denied when the URL breaks the target policy."}
	remove := admin("Delete a webhook endpoint", "Deletes the endpoint with its deliveries and delivery log. No body. Records webhook.deleted.", true, "204", "400", "401", "403", "404", "408", "503")
	paths["/api/v1/webhooks/{id}"] = map[string]any{"get": get, "put": replace, "delete": remove}

	rotate := admin("Rotate a webhook signing secret", "Issues a new secret and returns it once. For graceSeconds (default 86400, at most 604800; 0 drops it at once) the previous secret keeps signing too, so X-Jelee-Signature carries one v1 entry per secret (G12.4). Records webhook.secret_rotated without any secret.", true, "200", "400", "401", "403", "404", "408", "413", "415", "503")
	body(rotate, objectSchema(map[string]any{"graceSeconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "default": 86400}}))
	rotate["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("WebhookWithSecret"))
	paths["/api/v1/webhooks/{id}/rotate-secret"] = map[string]any{"post": rotate}

	test := admin("Send a test event", "Sends one signed system.alert event with data {\"test\": true} directly, disabled endpoints included. It is not retried and not logged as a delivery; the outcome is returned. The request deadline applies, so an endpoint slower than it is reported as timeout. The body must be an empty JSON object.", true, "200", "400", "401", "403", "404", "408", "413", "415", "503")
	body(test, map[string]any{"type": "object", "additionalProperties": false})
	test["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("WebhookTestResult"))
	paths["/api/v1/webhooks/{id}/test"] = map[string]any{"post": test}

	deliveries := admin("List webhook deliveries", "The delivery log (G12.3), newest first. state filters by pending, delivered or dead. Pass pagination.nextCursor as cursor for the next page; an empty nextCursor ends the listing. Settled events are deleted after the configured retention.", true, "200", "400", "401", "403", "404", "408", "503")
	deliveries["parameters"] = []any{idParameter(),
		map[string]any{"name": "state", "in": "query", "schema": state},
		map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}},
	}
	deliveries["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(objectSchema(map[string]any{
		"deliveries": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("WebhookDelivery")},
		"pagination": objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit"),
	}, "deliveries", "pagination"))
	paths["/api/v1/webhooks/{id}/deliveries"] = map[string]any{"get": deliveries}

	delivery := admin("Read a webhook delivery", "A delivery with its 100 most recent attempts.", false, "200", "400", "401", "403", "404", "408", "503")
	delivery["parameters"] = deliveryParameters
	delivery["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("WebhookDeliveryDetail"))
	paths["/api/v1/webhooks/{id}/deliveries/{deliveryId}"] = map[string]any{"get": delivery}

	replay := admin("Replay a webhook delivery", "Moves a dead or delivered delivery back to pending, due now, with a fresh attempt budget (G12.3). The consumer receives the same eventId again and deduplicates on it (G12.6). A pending delivery is 409. The body must be an empty JSON object. Records webhook.delivery_replayed.", false, "202", "400", "401", "403", "404", "408", "409", "413", "415", "503")
	replay["parameters"] = deliveryParameters
	body(replay, map[string]any{"type": "object", "additionalProperties": false})
	replay["responses"].(map[string]any)["202"].(map[string]any)["content"] = data(schemaRef("WebhookDelivery"))
	paths["/api/v1/webhooks/{id}/deliveries/{deliveryId}/replay"] = map[string]any{"post": replay}
}
