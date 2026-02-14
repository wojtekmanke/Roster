// Package server exposes Roster over HTTP: the GraphQL API, inbound triggers
// (webhooks and Pub/Sub push), the endpoints an external scheduler uses to
// drive a scale-to-zero deployment, and the dashboard's static files.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/graph"
	"github.com/wojtekmanke/roster/internal/store"
)

// maxBody bounds request bodies on the trigger endpoints.
const maxBody = 1 << 20

// Server holds the HTTP layer's dependencies.
type Server struct {
	Fleet *fleet.Service
	// Token, when set, must be presented as a bearer token on every endpoint
	// except the health check and the dashboard's static files.
	Token string
	// WebDir is the directory holding the built dashboard. Empty disables it.
	WebDir string
	Log    *slog.Logger
	// Mounts are extra handlers, such as the demo shop's API.
	Mounts map[string]http.Handler
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)

	mux.Handle("/graphql", s.auth(s.withActor(graph.NewHandler(s.Fleet, s.Log))))
	mux.Handle("POST /hooks/{agent}", s.auth(http.HandlerFunc(s.webhook)))
	mux.Handle("POST /events/pubsub", s.auth(http.HandlerFunc(s.pubsub)))
	mux.Handle("POST /internal/tick", s.auth(http.HandlerFunc(s.tick)))
	mux.Handle("POST /internal/work", s.auth(http.HandlerFunc(s.work)))

	for pattern, handler := range s.Mounts {
		mux.Handle(pattern, handler)
	}
	if s.WebDir != "" {
		// Registered for every method so the pattern does not conflict with
		// method-less routes such as /graphql; the handler itself is read-only.
		mux.Handle("/", s.dashboard())
	}
	return mux
}

// auth requires the configured bearer token. Pub/Sub push subscriptions
// cannot set headers, so the token is also accepted as a query parameter.
func (s *Server) auth(next http.Handler) http.Handler {
	if s.Token == "" {
		return next
	}
	want := []byte(s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="roster"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withActor names the caller for the audit trail. The header is trusted
// because it sits behind the token; in production an identity-aware proxy
// should set it.
func (s *Server) withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := r.Header.Get("X-Roster-User"); user != "" {
			r = r.WithContext(graph.WithActor(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.Fleet.Store.Pool.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	io.WriteString(w, "ok\n")
}

// webhook queues one run of an agent that declares a webhook trigger. The
// request body, if any, is the run's input.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("agent")
	a, err := s.Fleet.Store.Agent(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sp, err := a.Spec()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !sp.HasWebhook() {
		http.Error(w, "this agent has no webhook trigger", http.StatusNotFound)
		return
	}

	input, ok := readJSON(w, r)
	if !ok {
		return
	}
	switch err := s.Fleet.Trigger(r.Context(), name, fleet.TriggerWebhook, input, nil); {
	case errors.Is(err, fleet.ErrAgentNotActive):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		s.fail(w, r, err)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// pubsubPush is the envelope Google Cloud Pub/Sub posts to a push endpoint.
type pubsubPush struct {
	Message struct {
		Data       string            `json:"data"`
		Attributes map[string]string `json:"attributes"`
		MessageID  string            `json:"messageId"`
	} `json:"message"`
}

// pubsub turns a Pub/Sub push message into an event. The event name comes
// from the message's "event" attribute and the payload from its data.
//
// Malformed messages are acknowledged with 204 and dropped: answering with an
// error would only make Pub/Sub redeliver something that can never succeed.
func (s *Server) pubsub(w http.ResponseWriter, r *http.Request) {
	var push pubsubPush
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&push); err != nil {
		s.Log.Warn("pubsub: dropped an unreadable push request", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	event := push.Message.Attributes["event"]
	payload, err := base64.StdEncoding.DecodeString(push.Message.Data)
	if event == "" || err != nil || (len(payload) > 0 && !json.Valid(payload)) {
		s.Log.Warn("pubsub: dropped a message without an event attribute or JSON data", "message", push.Message.MessageID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := s.Fleet.Emit(r.Context(), event, payload, nil); err != nil {
		s.fail(w, r, err) // a 500 makes Pub/Sub retry
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tick runs the periodic housekeeping once. Point Cloud Scheduler at it when
// no instance is guaranteed to be running.
func (s *Server) tick(w http.ResponseWriter, r *http.Request) {
	if err := s.Fleet.Tick(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// work executes queued jobs within the request, up to ?max= of them
// (default 10), and reports how many it ran.
func (s *Server) work(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if raw := r.URL.Query().Get("max"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			http.Error(w, "max must be between 1 and 100", http.StatusBadRequest)
			return
		}
		limit = n
	}
	// Finish the job in hand even if the caller hangs up.
	done, err := s.Fleet.Drain(context.WithoutCancel(r.Context()), "http", limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"worked": done})
}

// dashboard serves the built single-page app, answering unknown paths with
// index.html so client-side routes survive a reload.
func (s *Server) dashboard() http.Handler {
	files := http.FileServer(http.Dir(s.WebDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := filepath.Join(s.WebDir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			if strings.Contains(filepath.Base(r.URL.Path), ".") {
				http.NotFound(w, r) // a missing asset, not a client-side route
				return
			}
			http.ServeFile(w, r, filepath.Join(s.WebDir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// readJSON reads an optional JSON body. It reports false after writing an
// error response.
func readJSON(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if len(body) == 0 {
		return nil, true
	}
	if !json.Valid(body) {
		http.Error(w, "request body must be JSON", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}
