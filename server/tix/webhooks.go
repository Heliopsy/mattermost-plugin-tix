// SPDX-License-Identifier: AGPL-3.0-only

package tix

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Webhook is a tenant's delivery endpoint (GET /webhooks). The secret is never returned.
type Webhook struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	URL        string    `json:"url"`
	EventTypes []string  `json:"event_types"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
}

// WebhookInput is the body of PUT /webhooks: an empty ID registers a new endpoint, an
// empty Secret keeps the existing one (or makes tix generate one).
type WebhookInput struct {
	ID         string   `json:"id,omitempty"`
	URL        string   `json:"url"`
	Secret     string   `json:"secret,omitempty"`
	EventTypes []string `json:"event_types"`
	Active     bool     `json:"active"`
}

// WebhookDelivery is one entry of the delivery log (GET /webhooks/deliveries).
type WebhookDelivery struct {
	ID             string    `json:"id"`
	EndpointID     string    `json:"endpoint_id"`
	EventSeq       int64     `json:"event_seq"`
	Attempts       int       `json:"attempts"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
	Status         string    `json:"status"`
	LastError      string    `json:"last_error"`
	LastStatusCode int       `json:"last_status_code"`
	CreatedAt      time.Time `json:"created_at"`
}

// ListWebhooks lists the tenant's endpoints. Needs webhook:admin.
func (c *Client) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	var p Page[Webhook]
	return p.Items, c.Do(ctx, http.MethodGet, "/webhooks", url.Values{"limit": {"500"}}, nil, &p)
}

// PutWebhook registers an endpoint, or updates the one in.ID names. Needs webhook:admin.
func (c *Client) PutWebhook(ctx context.Context, in WebhookInput) (*Webhook, error) {
	var w Webhook
	return &w, c.Do(ctx, http.MethodPut, "/webhooks", nil, in, &w)
}

// DeleteWebhook removes an endpoint and its pending deliveries. Needs webhook:admin.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/webhooks/"+url.PathEscape(id), nil, nil, nil)
}

// ListWebhookDeliveries lists one endpoint's most recent deliveries, newest first. Needs webhook:admin.
func (c *Client) ListWebhookDeliveries(ctx context.Context, endpointID string, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 10
	}
	q := url.Values{"endpoint_id": {endpointID}, "limit": {strconv.Itoa(limit)}, "direction": {"desc"}}
	var p Page[WebhookDelivery]
	return p.Items, c.Do(ctx, http.MethodGet, "/webhooks/deliveries", q, nil, &p)
}
