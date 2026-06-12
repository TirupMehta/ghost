# 👻 Ghost — Ephemeral, Encrypted, Zero-Knowledge CLI Chat

```
  ██████╗ ██╗  ██╗ ██████╗ ███████╗████████╗
  ██╔════╝ ██║  ██║██╔═══██╗██╔════╝╚══██╔══╝
  ██║  ███╗███████║██║   ██║███████╗   ██║
  ██║   ██║██╔══██║██║   ██║╚════██║   ██║
  ╚██████╔╝██║  ██║╚██████╔╝███████║   ██║
   ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝
```

**Ghost** is a production-grade, ephemeral CLI chat system with end-to-end AES-256-GCM
encryption. The relay server is **deliberately blind** — it only routes ciphertext and
never holds decryption keys. When the last user leaves a room, all history is
deleted from memory immediately.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Client A                    Client B                   │
│  ┌───────────────┐           ┌───────────────┐          │
│  │  ghost (CLI)  │           │  ghost (CLI)  │          │
│  │               │           │               │          │
│  │  AES key      │           │  AES key      │          │
│  │  (RAM only)   │           │  (RAM only)   │          │
│  └──────┬────────┘           └────────┬──────┘          │
│         │ WebSocket (ciphertext only) │                 │
│         ▼                             ▼                 │
│  ┌─────────────────────────────────────────────┐        │
│  │        Ghost Relay Server  (:8080)          │        │
│  │                                             │        │
│  │  In-memory rooms (sync.RWMutex protected)   │        │
│  │  Max 100-message rolling history per room   │        │
│  │  Room → annihilated when last peer leaves   │        │
│  └─────────────────────────────────────────────┘        │
└─────────────────────────────────────────────────────────┘
```

## Project Layout

```
ghost-cli/
├── server/
│   ├── go.mod
│   └── main.go          ← In-memory relay server
├── client/
│   ├── go.mod
│   └── main.go          ← Interactive CLI client
├── installer/
│   └── install.sh       ← Single-command bash installer
├── Makefile
└── README.md
```

---

## Build

**Prerequisites:** Go 1.21+

```bash
# Build both binaries
make all

# Or individually:
make server   # → ./ghost-server (or ghost-server.exe on Windows)
make client   # → ./ghost        (or ghost.exe on Windows)
```

---

## Running

Simply run the compiled client binary. It automatically starts the embedded relay server in the background:

```bash
./ghost
```

On first run, Ghost will prompt for your handle and save it to `~/.ghost/config.json`.

---

## Usage Flow

### Create a room

1. Select **[1] Create a new encrypted chat room**
2. Ghost automatically starts the background server, attempts UPnP router port mapping, and generates a 6-digit PIN alongside a secure `ghost://` Connection Token.
3. Share the 6-digit PIN (for local WiFi/LAN) or the Connection Token (for internet/WAN) with your contact.
4. Ghost derives the 32-byte AES-256 key locally via SHA-256 on the PIN.

### Join a room

1. Select **[2] Join an existing room with a token**
2. Enter the 6-digit PIN or paste the `ghost://` Connection Token.
3. If using a PIN, Ghost broadcasts a UDP discovery packet on the local network to locate the host. If using a Connection Token, it decodes the host IP and connects directly over the internet.
4. Ghost derives the identical AES-256 key locally and connects directly.

### Chat

- Type and press Enter to send
- Messages are AES-256-GCM encrypted before leaving your machine
- `/quit` or `/exit` to leave (or Ctrl+C)
- On exit, the AES key bytes are **zeroed in RAM** before the function returns

---

## Security Properties

| Property | Implementation |
|---|---|
| End-to-end encryption | AES-256-GCM, fresh nonce per message |
| Key never transmitted | Key is derived locally via SHA-256; server only receives PIN and never sees or derives the AES key |
| Server is message-blind | Server stores and routes raw ciphertext hex |
| Ephemeral rooms | All history deleted when last peer disconnects |
| GC-assisted wipe | `runtime.GC()` called after room annihilation |
| AES key zeroing | `zeroKey()` overwrites key bytes before return |
| Concurrency safety | `sync.RWMutex` on all shared state; per-conn write mutex |
| Half-open TCP detection | Server pings every 30s; client pings every 25s; 90s read deadlines |
| Reconnect logic | 3 retries with exponential back-off before returning to menu |
| Cryptographically-secure PIN | `crypto/rand` via `math/big.Int` |

---

## Installer

Distribute and install the client using the single-command installer hosted directly on your server:

#### macOS / Linux (Bash)
```bash
curl -fsSL https://ghost.tirup.in/install.sh | bash
```

#### Windows (PowerShell)
```powershell
irm https://ghost.tirup.in/install.ps1 | iex
```

The installer:
1. Detects OS + CPU architecture (or defaults to Windows x64)
2. Downloads the correct pre-built binary
3. Installs to the local execution path (`~/.local/bin` on Unix, `~/.ghost/bin` on Windows) and adds it to the user PATH
4. Runs handle configuration setup on first install

### Uninstall

To uninstall the client and wipe all local configuration, simply run:

```bash
ghost uninstall
```

---

## Configuration

Config file: `~/.ghost/config.json`

```json
{
  "handle": "yourname"
}
```

Run `ghost` and select **[3] Change handle** to reset it at any time.

---

## Deploying the Server

The server is a single stateless binary with no external dependencies.

```bash
# Example systemd unit
[Unit]
Description=Ghost Relay Server
After=network.target

[Service]
ExecStart=/usr/local/bin/ghost-server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

> **Note:** By default, the client is configured to connect to your local device:
> ```go
> var serverAddr = "http://localhost:8080"
> ```
> Users can override this locally to connect to any peer/custom server by setting the `GHOST_SERVER` environment variable (e.g. `GHOST_SERVER=http://192.168.1.50:8080`). The client automatically uses `wss://` for HTTPS and `ws://` for HTTP.
