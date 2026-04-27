# Detect Windows so binary extensions and env-var syntax are set correctly.
ifeq ($(OS),Windows_NT)
  EXE        := .exe
  XWIN       := set GOOS=windows&& set GOARCH=amd64&&
  XLIN       := set GOOS=linux&& set GOARCH=amd64&&
  MKDIR_BUILD = if not exist "$(subst /,\,$(AGENT_BUILD_DIR))" mkdir "$(subst /,\,$(AGENT_BUILD_DIR))"
else
  EXE        :=
  XWIN       := GOOS=windows GOARCH=amd64
  XLIN       := GOOS=linux GOARCH=amd64
  MKDIR_BUILD = mkdir -p "$(AGENT_BUILD_DIR)"
endif

MODULE := github.com/aelder202/sable
SERVER_BINARY := sable-server$(EXE)
WINDOWS_SERVER_BINARY := sable-server.exe

# Pick up generated values from the selected agent env file if it exists.
# Run 'make setup SERVER_URL=https://<public-server-ip>:443' to create config.env.
AGENT_ENV ?= config.env
-include $(AGENT_ENV)

AGENT_ID         ?= changeme-agent-id
AGENT_SECRET_HEX ?= changeme-secret-hex
SERVER_URL       ?= https://127.0.0.1:443
CERT_FP_HEX      ?= changeme-fingerprint-hex
SLEEP_SECONDS    ?= 30
DNS_DOMAIN       ?=
PROFILE          ?=

# Fall back to the first hyphen-separated chunk of AGENT_ID (the first 8 hex
# chars of a standard UUID) when AGENT_LABEL is absent. This keeps env files
# written before the label field was introduced working without migration.
ifeq ($(AGENT_LABEL),)
  AGENT_LABEL := $(word 1,$(subst -, ,$(AGENT_ID)))
endif

AGENT_BUILD_DIR        := builds/$(AGENT_LABEL)
AGENT_LINUX_ARTIFACT   := $(AGENT_BUILD_DIR)/agent-linux
AGENT_WINDOWS_ARTIFACT := $(AGENT_BUILD_DIR)/agent.exe

# -s -w strips symbol table and DWARF info from agent binaries.
STRIP   := -s -w
LDFLAGS := $(STRIP) \
           -X '$(MODULE)/internal/agent.AgentID=$(AGENT_ID)' \
           -X '$(MODULE)/internal/agent.SecretHex=$(AGENT_SECRET_HEX)' \
           -X '$(MODULE)/internal/agent.ServerURL=$(SERVER_URL)' \
           -X '$(MODULE)/internal/agent.CertFingerprintHex=$(CERT_FP_HEX)' \
           -X '$(MODULE)/internal/agent.SleepSecondsStr=$(SLEEP_SECONDS)' \
           -X '$(MODULE)/internal/agent.DNSDomainStr=$(DNS_DOMAIN)'

.PHONY: setup build build-windows-server register build-server build-agent-linux build-agent-windows test test-integration gen-secret
.PRECIOUS: register-tool$(EXE)

## First-time setup: generates config.env, server.crt, server.key.
## Usage: make setup SERVER_URL=https://<public-server-ip>:443 [LABEL=<label>] [PROFILE=fast|quiet|dns] [DNS_DOMAIN=example.com]
setup:
	go run ./tools/setup $(if $(LABEL),--label "$(LABEL)") $(if $(PROFILE),--profile "$(PROFILE)") $(if $(DNS_DOMAIN),--dns-domain "$(DNS_DOMAIN)")

## Build the recommended bundle for this machine: host-native Sable server + a per-agent Linux binary.
build:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run 'make setup SERVER_URL=https://<public-server-ip>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	go build -o $(SERVER_BINARY) ./cmd/server
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	@echo [+] Built: $(SERVER_BINARY) and $(AGENT_LINUX_ARTIFACT)

## Cross-build a Windows Sable server bundle plus a per-agent Linux binary from a Linux/macOS build host.
## Produces: $(WINDOWS_SERVER_BINARY) (Windows) + $(AGENT_LINUX_ARTIFACT)
build-windows-server:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run 'make setup SERVER_URL=https://<public-server-ip>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(XWIN) go build -o $(WINDOWS_SERVER_BINARY) ./cmd/server
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	@echo [+] Built: $(WINDOWS_SERVER_BINARY) and $(AGENT_LINUX_ARTIFACT)

## Register the current agent or create/register a new one with NEW=1.
## Usage: make register PASSWORD=yourpassword [NEW=1] [LABEL=<label>]
register:
	@go run ./tools/register $(if $(NEW),--new) $(if $(LABEL),--label "$(LABEL)") --server-url "$(SERVER_URL)" --cert-fp "$(CERT_FP_HEX)" --sleep-seconds "$(SLEEP_SECONDS)" --dns-domain "$(DNS_DOMAIN)" --output-dir "agents" "$(AGENT_ID)" "$(AGENT_SECRET_HEX)" "$(PASSWORD)"

register-tool$(EXE): tools/register/main.go tools/register/main_test.go go.mod go.sum
	go build -o register-tool$(EXE) ./tools/register

build-server:
	go build -o $(SERVER_BINARY) ./cmd/server

build-agent-linux:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run 'make setup SERVER_URL=https://<public-server-ip>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	@echo [+] Built: $(AGENT_LINUX_ARTIFACT)

build-agent-windows:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run 'make setup SERVER_URL=https://<public-server-ip>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(MKDIR_BUILD)
	$(XWIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_WINDOWS_ARTIFACT)" ./cmd/agent
	@echo [+] Built: $(AGENT_WINDOWS_ARTIFACT)

test:
	go test ./...

test-integration:
	go test -tags integration -v ./...

gen-secret:
	@go run ./tools/gensecret

