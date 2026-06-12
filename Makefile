# Ghost CLI — Ephemeral Encrypted Chat
# ───────────────────────────────────────
# Targets
#   make server        – build relay server binary
#   make client        – build ghost client binary
#   make all           – build both
#   make run-server    – start the relay server (port 8080)
#   make clean         – remove compiled binaries

GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# Output binary names change per platform
ifeq ($(GOOS),windows)
  SERVER_OUT = ghost-server.exe
  CLIENT_OUT = ghost.exe
else
  SERVER_OUT = ghost-server
  CLIENT_OUT = ghost
endif

.PHONY: all server client run-server clean tidy

all: server client

server:
	@echo "→  Building relay server ($(GOOS)/$(GOARCH))..."
	cd server && go mod tidy && go build -ldflags="-s -w" -o ../$(SERVER_OUT) .
	@echo "✓  $(SERVER_OUT) ready"

client:
	@echo "→  Building Ghost client ($(GOOS)/$(GOARCH))..."
	cd client && go mod tidy && go build -ldflags="-s -w" -o ../$(CLIENT_OUT) .
	@echo "✓  $(CLIENT_OUT) ready"

run-server: server
	@echo "→  Starting Ghost relay server on :8080"
	./$(SERVER_OUT)

tidy:
	cd server && go mod tidy
	cd client && go mod tidy

clean:
	rm -f ghost-server ghost-server.exe ghost ghost.exe
	@echo "✓  Cleaned"
