// SPDX-License-Identifier: AGPL-3.0-only

// Package api serves the plugin's HTTP routes under /plugins/{plugin_id}.
// Features mount their own routes on Public (no Mattermost session: webhooks,
// each proving itself) or User (an authenticated Mattermost user: dialogs,
// message actions, autocomplete and the webapp).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi/experimental/bot/logger"
)

// Handler routes the requests the server forwards to the plugin's ServeHTTP hook.
type Handler struct {
	pluginAPI plugin.API
	router    *mux.Router
	// Public is for callers without a Mattermost session; each route authenticates itself.
	Public *mux.Router
	// User requires Mattermost-User-ID, which the server sets only after authenticating.
	User *mux.Router
}

// NewHandler returns the router with its middleware.
func NewHandler(pluginAPI plugin.API) *Handler {
	h := &Handler{pluginAPI: pluginAPI}
	h.router = mux.NewRouter()
	h.router.Use(h.logRequests, h.recoverPanic, limitRequestBody, setSecurityHeaders)
	h.Public = h.router.PathPrefix("/public").Subrouter()
	h.User = h.router.PathPrefix("/api/v1").Subrouter()
	h.User.Use(h.MattermostAuthorizationRequired)
	return h
}

type pluginContextKey struct{}

// ServeHTTP has the plugin hook's signature; the plugin's ServeHTTP passes its arguments straight through.
func (h *Handler) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pluginContextKey{}, c)))
}

// MattermostAuthorizationRequired can trust Mattermost-User-ID because the server sets it only after authenticating the request.
// Requests from other plugins arrive without a user ID and are rejected here; routes meant for
// them should check Mattermost-Plugin-ID against an allowlist instead.
func (h *Handler) MattermostAuthorizationRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("Mattermost-User-ID")
		if userID == "" {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// maxRequestBodyBytes bounds how much of a request body a handler will read.
// Raise it for endpoints that legitimately accept larger payloads.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

type loggerContextKey struct{}

// requestLogger returns the logger attached by logRequests, which tags every line with the requesting user.
func requestLogger(r *http.Request) logger.Logger {
	return r.Context().Value(loggerContextKey{}).(logger.Logger)
}

// logRequests attaches a request-scoped logger and logs every request at debug level,
// since the server doesn't log plugin requests.
func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log := logger.New(h.pluginAPI).With(logger.LogContext{
			"user_id": r.Header.Get("Mattermost-User-ID"),
		})
		r = r.WithContext(context.WithValue(r.Context(), loggerContextKey{}, log))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// The query string is omitted because it may carry tokens or other secrets.
		log.With(logger.LogContext{
			"method":   r.Method,
			"path":     r.URL.Path,
			"status":   rec.status,
			"duration": time.Since(start).String(),
		}).Debugf("HTTP request")
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records only the first status, since later ones never reach the client.
func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// recoverPanic stops a panicking handler from crashing the plugin process.
// The server deactivates a plugin that crashes repeatedly.
func (h *Handler) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if panicVal := recover(); panicVal != nil {
				requestLogger(r).With(logger.LogContext{
					"path":  r.URL.Path,
					"panic": fmt.Sprint(panicVal),
					"stack": string(debug.Stack()),
				}).Errorf("Recovered from panic in HTTP handler")
				// Once the response has started, an error body would be appended to it.
				if !rec.wroteHeader {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}


// UserID is the authenticated Mattermost user of a User route.
func UserID(r *http.Request) string { return r.Header.Get("Mattermost-User-ID") }

// PluginContext returns the plugin context of the request.
func PluginContext(r *http.Request) *plugin.Context {
	c, _ := r.Context().Value(pluginContextKey{}).(*plugin.Context)
	return c
}

// Log returns the request-scoped logger.
func Log(r *http.Request) logger.Logger { return requestLogger(r) }

// WriteJSON answers v as JSON.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		requestLogger(r).WithError(err).Warnf("Failed to write response")
	}
}

// WriteError answers {"error": msg}; err, when set, is logged and not sent.
func WriteError(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	if err != nil {
		requestLogger(r).WithError(err).Warnf("%s", msg)
	}
	WriteJSON(w, r, status, map[string]string{"error": msg})
}

