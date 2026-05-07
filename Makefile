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
SABLECTL_BINARY := sablectl$(EXE)

# Pick up generated values from the selected agent env file if it exists.
# config.env is created by 'sablectl install --url https://<host>:443'.
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
RESTRICT := go run ./tools/restrictfile
LDFLAGS := $(STRIP) \
           -X '$(MODULE)/internal/agent.AgentID=$(AGENT_ID)' \
           -X '$(MODULE)/internal/agent.SecretHex=$(AGENT_SECRET_HEX)' \
           -X '$(MODULE)/internal/agent.ServerURL=$(SERVER_URL)' \
           -X '$(MODULE)/internal/agent.CertFingerprintHex=$(CERT_FP_HEX)' \
           -X '$(MODULE)/internal/agent.SleepSecondsStr=$(SLEEP_SECONDS)' \
           -X '$(MODULE)/internal/agent.DNSDomainStr=$(DNS_DOMAIN)'

.PHONY: sablectl build build-windows-server build-server build-agent-linux build-agent-windows update-peas test test-integration gen-secret validate-openapi

## Build the unified sablectl installer/operator helper.
sablectl:
	go build -o $(SABLECTL_BINARY) ./cmd/sablectl

## Build the recommended bundle for this machine: host-native Sable server + a per-agent Linux binary.
build:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run './sablectl install --url https://<host>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	go build -o $(SERVER_BINARY) ./cmd/server
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	$(RESTRICT) "$(AGENT_LINUX_ARTIFACT)"
	@echo [+] Built: $(SERVER_BINARY) and $(AGENT_LINUX_ARTIFACT)

## Cross-build a Windows Sable server bundle plus a per-agent Linux binary from a Linux/macOS build host.
## Produces: $(WINDOWS_SERVER_BINARY) (Windows) + $(AGENT_LINUX_ARTIFACT)
build-windows-server:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run './sablectl install --url https://<host>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(XWIN) go build -o $(WINDOWS_SERVER_BINARY) ./cmd/server
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	$(RESTRICT) "$(AGENT_LINUX_ARTIFACT)"
	@echo [+] Built: $(WINDOWS_SERVER_BINARY) and $(AGENT_LINUX_ARTIFACT)

build-server:
	go build -o $(SERVER_BINARY) ./cmd/server

build-agent-linux:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run './sablectl install --url https://<host>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(MKDIR_BUILD)
	$(XLIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_LINUX_ARTIFACT)" ./cmd/agent
	$(RESTRICT) "$(AGENT_LINUX_ARTIFACT)"
	@echo [+] Built: $(AGENT_LINUX_ARTIFACT)

build-agent-windows:
ifeq ($(wildcard $(AGENT_ENV)),)
	$(error $(AGENT_ENV) not found - run './sablectl install --url https://<host>:443' first or pass AGENT_ENV=agents/<label>.env)
endif
	$(MKDIR_BUILD)
	$(XWIN) go build -ldflags "$(LDFLAGS)" -o "$(AGENT_WINDOWS_ARTIFACT)" ./cmd/agent
	$(RESTRICT) "$(AGENT_WINDOWS_ARTIFACT)"
	@echo [+] Built: $(AGENT_WINDOWS_ARTIFACT)

update-peas:
	go run ./tools/updatepeas

test:
	go test ./...

test-integration:
	go test -tags integration -v ./...

gen-secret:
	@go run ./tools/gensecret

## Lint docs/openapi.yaml using Redocly CLI. Requires Node.js (uses npx; nothing committed).
validate-openapi:
	npx --yes @redocly/cli@latest lint docs/openapi.yaml
