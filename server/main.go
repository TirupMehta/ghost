package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ──────────────────────────────────────────────
//  Domain Types
// ──────────────────────────────────────────────

// Message is the wire format exchanged between clients via the relay.
// The server is deliberately message-blind: it never holds the AES key and
// never decrypts the Ciphertext field.
type Message struct {
	Timestamp   string `json:"ts"`
	Sender      string `json:"sender"`
	PayloadType string `json:"type"` // "handshake" | "message" | "leave" | "ping" | "pong"
	Ciphertext  string `json:"ct"`   // hex-encoded AES-GCM ciphertext; empty for control frames
}

// conn wraps a gorilla WebSocket connection with a dedicated write mutex so
// that multiple goroutines can safely fan-out messages to the same peer.
type conn struct {
	mu sync.Mutex
	ws *websocket.Conn
}

// writeJSON serialises v and sends it over the connection while holding the
// per-connection write lock.
func (c *conn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteJSON(v)
}

// writePing sends a WebSocket-level ping frame.
func (c *conn) writePing() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.PingMessage, []byte("ghost-ping"))
}

// Room is the in-memory chat session.
// All exported fields are deliberately unexported to keep the struct internal.
type room struct {
	pin     string
	mu      sync.RWMutex
	conns   []*conn
	history []Message // rolling window – max historyLimit entries
}

const historyLimit = 100

// addConn registers a new connection in the room and returns its index.
func (r *room) addConn(c *conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns = append(r.conns, c)
}

// removeConn removes the given connection from the room's slice and returns
// the remaining count.
func (r *room) removeConn(target *conn) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := make([]*conn, 0, len(r.conns))
	for _, c := range r.conns {
		if c != target {
			updated = append(updated, c)
		}
	}
	r.conns = updated
	return len(r.conns)
}

// appendHistory records a message in the rolling history slice.
func (r *room) appendHistory(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = append(r.history, m)
	if len(r.history) > historyLimit {
		r.history = r.history[len(r.history)-historyLimit:]
	}
}

// fanOut broadcasts msg to every connection in the room except the sender.
func (r *room) fanOut(msg Message, sender *conn) {
	r.mu.RLock()
	targets := make([]*conn, len(r.conns))
	copy(targets, r.conns)
	r.mu.RUnlock()

	for _, c := range targets {
		if c == sender {
			continue
		}
		if err := c.writeJSON(msg); err != nil {
			log.Printf("fanOut write error to peer: %v", err)
		}
	}
}

// snapshotHistory returns a copy of the current history slice for safe
// transmission without holding the read lock for the duration of the send.
func (r *room) snapshotHistory() []Message {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap := make([]Message, len(r.history))
	copy(snap, r.history)
	return snap
}

// ──────────────────────────────────────────────
//  Server State
// ──────────────────────────────────────────────

type server struct {
	mu    sync.RWMutex
	rooms map[string]*room
}

func newServer() *server {
	return &server{rooms: make(map[string]*room)}
}

// createRoom generates a cryptographically-secure 6-digit PIN, allocates a new
// room, and stores it in the server map.  Returns the allocated PIN.
func (s *server) createRoom() (string, error) {
	for {
		pin, err := randomPIN()
		if err != nil {
			return "", fmt.Errorf("pin generation: %w", err)
		}

		s.mu.Lock()
		_, exists := s.rooms[pin]
		if !exists {
			s.rooms[pin] = &room{pin: pin}
			s.mu.Unlock()
			return pin, nil
		}
		s.mu.Unlock()
		// collision – try again
	}
}

// getRoom returns the room for the given PIN or nil if not found.
func (s *server) getRoom(pin string) *room {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rooms[pin]
}

// annihilate deletes the room from the server map and triggers GC so that the
// memory holding chat history is reclaimed promptly.
func (s *server) annihilate(pin string) {
	s.mu.Lock()
	delete(s.rooms, pin)
	s.mu.Unlock()
	runtime.GC()
	log.Printf("room %s annihilated – memory wiped", pin)
}

// ──────────────────────────────────────────────
//  Helpers
// ──────────────────────────────────────────────

func randomPIN() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ──────────────────────────────────────────────
//  HTTP Handlers
// ──────────────────────────────────────────────

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin:      func(_ *http.Request) bool { return true },
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
}

// handleCreate handles POST /room/create – returns a JSON body containing the
// allocated PIN.
func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pin, err := s.createRoom()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Printf("createRoom error: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"pin": pin})
	log.Printf("room created: %s", pin)
}

// handleWS handles GET /room/ws?pin=<PIN> – upgrades to WebSocket and drives
// the full lifecycle of one client connection inside a single goroutine.
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	pin := r.URL.Query().Get("pin")
	if pin == "" {
		http.Error(w, "pin required", http.StatusBadRequest)
		return
	}

	rm := s.getRoom(pin)
	if rm == nil {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}

	// Configure WebSocket keepalive from the server side.
	wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))
	wsConn.SetPongHandler(func(_ string) error {
		wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	peer := &conn{ws: wsConn}
	rm.addConn(peer)
	log.Printf("peer joined room %s (addr: %s)", pin, wsConn.RemoteAddr())

	// Flush existing history to the newly joined peer.
	go func() {
		for _, msg := range rm.snapshotHistory() {
			if err := peer.writeJSON(msg); err != nil {
				log.Printf("history flush error: %v", err)
				return
			}
		}
	}()

	// Start a ticker that sends server-side ping frames to detect half-open
	// TCP connections.  The ticker goroutine is stopped when this handler
	// returns via the done channel.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := peer.writePing(); err != nil {
					log.Printf("ping error (room %s): %v", pin, err)
					wsConn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Read loop – runs until the client disconnects or sends a leave frame.
	defer func() {
		close(done)
		wsConn.Close()

		remaining := rm.removeConn(peer)
		log.Printf("peer left room %s – %d remaining", pin, remaining)

		if remaining == 0 {
			s.annihilate(pin)
		}
	}()

	for {
		var msg Message
		if err := wsConn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseNoStatusReceived) {
				log.Printf("unexpected close in room %s: %v", pin, err)
			}
			return
		}

		// Refresh read deadline on every legitimate message received.
		wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))

		if msg.PayloadType == "ping" {
			// Application-level ping from client; reply with pong.
			pong := Message{
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				PayloadType: "pong",
			}
			_ = peer.writeJSON(pong)
			continue
		}

		if msg.PayloadType == "leave" {
			log.Printf("graceful leave in room %s from %s", pin, msg.Sender)
			return
		}

		if msg.PayloadType == "message" {
			rm.appendHistory(msg)
			rm.fanOut(msg, peer)
		}
	}
}

// handleHealth is a simple liveness probe endpoint.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ──────────────────────────────────────────────
//  Entry Point
// ──────────────────────────────────────────────

func main() {
	srv := newServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/room/create", srv.handleCreate)
	mux.HandleFunc("/room/ws", srv.handleWS)
	mux.HandleFunc("/health", handleHealth)

	// Serve the install script directly
	mux.HandleFunc("/install.sh", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./installer/install.sh")
	})

	// Serve the Windows PowerShell install script directly
	mux.HandleFunc("/install.ps1", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./installer/install.ps1")
	})

	// Serve the uninstall script directly
	mux.HandleFunc("/uninstall.sh", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./installer/uninstall.sh")
	})

	// Serve the Windows PowerShell uninstall script directly
	mux.HandleFunc("/uninstall.ps1", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./installer/uninstall.ps1")
	})

	// Serve compiled binaries for download
	mux.Handle("/releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir("./releases"))))

	addr := ":8080"
	log.Printf("ghost relay server starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // WebSocket connections are long-lived; disable write timeout
		IdleTimeout:  120 * time.Second,
	}

	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
