// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
)

// Delivery headers, as tix's internal/webhook package sets them.
const (
	headerEvent     = "X-Tix-Event"
	headerDelivery  = "X-Tix-Delivery"
	headerTimestamp = "X-Tix-Timestamp"
	headerSignature = "X-Tix-Signature"
	signaturePrefix = "sha256="
)

// maxSkew is how far a delivery's timestamp may be from now. tix signs every attempt,
// retries and redeliveries included, at send time, so a fresh delivery is never older.
const maxSkew = 5 * time.Minute

var (
	errNoSecret  = errors.New("webhook secret is not configured")
	errSignature = errors.New("signature does not match")
	errStale     = errors.New("timestamp is missing or outside the allowed window")
)

// sign is tix's scheme: "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// verify checks a delivery's signature in constant time and rejects a timestamp outside maxSkew.
func verify(secret, timestamp, signature string, body []byte, now time.Time) error {
	if secret == "" {
		return errNoSecret
	}
	if !hmac.Equal([]byte(sign(secret, timestamp, body)), []byte(signature)) {
		return errSignature
	}
	ts, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return errStale
	}
	if d := now.Sub(ts); d > maxSkew || d < -maxSkew {
		return errStale
	}
	return nil
}

// Event is one tix event as a webhook delivers it: the JSON of tix's core.Event.
type Event struct {
	Seq         int64          `json:"seq"`
	ID          string         `json:"id"`
	TenantID    string         `json:"tenant_id"`
	Type        string         `json:"type"`
	ProjectID   string         `json:"project_id"`
	SubjectType string         `json:"subject_type"`
	SubjectID   string         `json:"subject_id"`
	ActorID     string         `json:"actor_id"`
	Payload     map[string]any `json:"payload"`
	OccurredAt  time.Time      `json:"occurred_at"`
}

// Str reads a string from the payload.
func (e *Event) Str(key string) string {
	s, _ := e.Payload[key].(string)
	return s
}

// Bool reads a boolean from the payload.
func (e *Event) Bool(key string) bool {
	b, _ := e.Payload[key].(bool)
	return b
}

// Strings reads a list of strings from the payload.
func (e *Event) Strings(key string) []string {
	raw, _ := e.Payload[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Ref is the task reference the event concerns, e.g. infra-42, or "".
func (e *Event) Ref() string { return e.Str("ref") }

func parseEvent(body []byte) (*Event, error) {
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("decoding event: %w", err)
	}
	if ev.Type == "" || ev.ID == "" {
		return nil, errors.New("event has no type or id")
	}
	return &ev, nil
}

// webhook receives a tix delivery: verify, parse, drop repeats, answer, then work in the background.
func (f *Feature) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.WriteError(w, r, http.StatusRequestEntityTooLarge, "body too large", nil)
		return
	}
	if verr := verify(f.a.Config().WebhookSecret, r.Header.Get(headerTimestamp), r.Header.Get(headerSignature), body, f.now()); verr != nil {
		api.Log(r).Warnf("tix webhook rejected: %s", verr.Error())
		api.WriteError(w, r, http.StatusUnauthorized, "invalid signature", nil)
		return
	}
	ev, err := parseEvent(body)
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad event", err)
		return
	}
	if !f.firstSight(ev.ID) {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go f.safeHandle(ev)
}

// safeHandle processes one event off the request, so a panic never takes the plugin down.
func (f *Feature) safeHandle(ev *Event) {
	defer func() {
		if p := recover(); p != nil {
			f.a.API.LogError("tix: panic while handling a webhook event", "type", ev.Type, "panic", fmt.Sprint(p))
		}
	}()
	f.handle(ev)
}
