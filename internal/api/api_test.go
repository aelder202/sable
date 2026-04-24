package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/api"
	"github.com/aelder202/sable/internal/session"
	"github.com/golang-jwt/jwt/v5"
)

const testJWTSecret = "test-jwt-secret-32-bytes-padding"

func setupAPI(t *testing.T) (*api.Router, *session.Store) {
	t.Helper()
	store := session.NewStore()
	store.Register(&session.Agent{
		ID:       "agent-1",
		Secret:   []byte("secret"),
		Hostname: "victim",
		OS:       "linux",
		Arch:     "amd64",
		LastSeen: time.Now(),
	})
	cfg := &api.Config{
		OperatorPasswordHash: api.HashPassword("testpassword"),
		JWTSecret:            []byte(testJWTSecret),
	}
	return api.NewRouter(store, cfg), store
}

// doRequest performs an in-process HTTP request against the router using httptest.NewRecorder.
func doRequest(t *testing.T, router http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// loginAndGetToken performs a login request in-process and returns the JWT token.
func loginAndGetToken(t *testing.T, router http.Handler) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": "testpassword"})
	w := doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
		"Content-Type": "application/json",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d", w.Code)
	}
	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result) //nolint:errcheck
	token := result["token"]
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	return token
}

func signToken(t *testing.T, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("SignedString failed: %v", err)
	}
	return signed
}

func TestLoginSuccess(t *testing.T) {
	router, _ := setupAPI(t)
	loginAndGetToken(t, router) // fails if token empty
}

func TestLoginWrongPassword(t *testing.T) {
	router, _ := setupAPI(t)
	body, _ := json.Marshal(map[string]string{"password": "wrongpassword"})
	w := doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
		"Content-Type": "application/json",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestLoginRejectsNonJSONContentType(t *testing.T) {
	router, _ := setupAPI(t)
	body := []byte(`{"password":"testpassword"}`)
	w := doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
		"Content-Type": "text/plain",
	})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", w.Code)
	}
}

func TestLoginRejectsTrailingJSON(t *testing.T) {
	router, _ := setupAPI(t)
	body := []byte(`{"password":"testpassword"}{"extra":true}`)
	w := doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
		"Content-Type": "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestListAgentsRequiresAuth(t *testing.T) {
	router, _ := setupAPI(t)
	w := doRequest(t, router, http.MethodGet, "/api/agents", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestListAgentsWithAuth(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	w := doRequest(t, router, http.MethodGet, "/api/agents", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestListAgentsRejectsWrongAudienceToken(t *testing.T) {
	router, _ := setupAPI(t)
	token := signToken(t, jwt.RegisteredClaims{
		Subject:   "operator",
		Issuer:    "sable-operator",
		Audience:  jwt.ClaimStrings{"not-the-web-ui"},
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		NotBefore: jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	w := doRequest(t, router, http.MethodGet, "/api/agents", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestQueueTask(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "shell", "payload": "whoami"})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 queuing task, got %d", w.Code)
	}
}

func TestQueueInvalidTaskType(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "badtype", "payload": ""})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid task type, got %d", w.Code)
	}
}

func TestQueueTaskRejectsInvalidSleepValue(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "sleep", "payload": "0"})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid sleep value, got %d", w.Code)
	}
}

func TestQueueTaskRejectsKillPayload(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "kill", "payload": "now"})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for kill payload, got %d", w.Code)
	}
}

func TestQueueTaskRejectsInvalidUploadPayload(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "upload", "payload": "/tmp/file:not-base64"})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid upload payload, got %d", w.Code)
	}
}

func TestQueueTaskNormalizesInteractivePayload(t *testing.T) {
	router, store := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "interactive", "payload": " start "})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for normalized interactive payload, got %d", w.Code)
	}
	task := store.DequeueTask("agent-1")
	if task == nil || task.Payload != "start" {
		t.Fatalf("expected normalized payload %q, got %#v", "start", task)
	}
}

func TestQueueTaskNormalizesSleepPayload(t *testing.T) {
	router, store := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{"type": "sleep", "payload": " 60 "})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for normalized sleep payload, got %d", w.Code)
	}
	task := store.DequeueTask("agent-1")
	if task == nil || task.Payload != "60" {
		t.Fatalf("expected normalized payload %q, got %#v", "60", task)
	}
}

func TestRegisterAgent(t *testing.T) {
	router, store := setupAPI(t)
	token := loginAndGetToken(t, router)

	// Valid registration.
	body, _ := json.Marshal(map[string]string{
		"id":         "new-agent-123",
		"secret_hex": "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
	})
	w := doRequest(t, router, http.MethodPost, "/api/agents", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on agent registration, got %d", w.Code)
	}
	if _, ok := store.Get("new-agent-123"); !ok {
		t.Fatal("agent not found in store after registration")
	}
}

func TestRegisterAgentInvalidSecret(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)

	body, _ := json.Marshal(map[string]string{"id": "x", "secret_hex": "tooshort"})
	w := doRequest(t, router, http.MethodPost, "/api/agents", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short secret, got %d", w.Code)
	}
}

func TestGetTaskOutputs(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	w := doRequest(t, router, http.MethodGet, "/api/agents/agent-1/tasks", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for task outputs, got %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	w := doRequest(t, router, http.MethodGet, "/api/agents", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options: DENY")
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Error("missing Content-Security-Policy header")
	}
	if w.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Error("missing Cross-Origin-Opener-Policy: same-origin")
	}
	if w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Error("missing Cross-Origin-Resource-Policy: same-origin")
	}
	if w.Header().Get("X-Permitted-Cross-Domain-Policies") != "none" {
		t.Error("missing X-Permitted-Cross-Domain-Policies: none")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing Cache-Control: no-store")
	}
}

func TestLoginRejectsOversizedBody(t *testing.T) {
	router, _ := setupAPI(t)
	body := []byte(`{"password":"` + strings.Repeat("a", 5000) + `"}`)
	w := doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
		"Content-Type": "application/json",
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized login body, got %d", w.Code)
	}
}

func TestLoginRateLimitSetsRetryAfter(t *testing.T) {
	router, _ := setupAPI(t)
	body, _ := json.Marshal(map[string]string{"password": "wrongpassword"})
	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = doRequest(t, router, http.MethodPost, "/api/auth/login", body, map[string]string{
			"Content-Type": "application/json",
		})
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last.Code)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on login rate limit")
	}
}

func TestQueueTaskPayloadTooLarge(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)
	body, _ := json.Marshal(map[string]string{
		"type":    "upload",
		"payload": strings.Repeat("A", 49*1024),
	})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized task payload, got %d", w.Code)
	}
}

func TestQueueTaskReturns429WhenQueueFull(t *testing.T) {
	router, _ := setupAPI(t)
	token := loginAndGetToken(t, router)

	for i := 0; i < 64; i++ {
		body, _ := json.Marshal(map[string]string{"type": "shell", "payload": "id"})
		w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 while filling queue, got %d at %d", w.Code, i)
		}
	}

	body, _ := json.Marshal(map[string]string{"type": "shell", "payload": "id"})
	w := doRequest(t, router, http.MethodPost, "/api/agents/agent-1/task", body, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when queue is full, got %d", w.Code)
	}
}
