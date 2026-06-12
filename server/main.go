package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"
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
// allocated PIN and public/local IP details.
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

	ip := getPublicIP()
	if ip == "" {
		ip = getLocalIP()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"pin":  pin,
		"ip":   ip,
		"port": "8080",
	})
	log.Printf("room created: %s (IP: %s)", pin, ip)
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

// startUDPResponder listens on UDP port 9090 for client discovery broadcasts.
// If a broadcast matches a running PIN, it replies to confirm.
func startUDPResponder(srv *server) {
	addr, err := net.ResolveUDPAddr("udp", ":9090")
	if err != nil {
		log.Printf("UDP resolve error: %v", err)
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("UDP listen error: %v", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("UDP read error: %v", err)
			continue
		}

		msg := string(buf[:n])
		if strings.HasPrefix(msg, "GHOST_DISCOVER:") {
			pin := strings.TrimPrefix(msg, "GHOST_DISCOVER:")
			if r := srv.getRoom(pin); r != nil {
				reply := "GHOST_RESPONSE:" + pin
				_, _ = conn.WriteToUDP([]byte(reply), remoteAddr)
			}
		}
	}
}

// ──────────────────────────────────────────────
//  Entry Point
// ──────────────────────────────────────────────

func main() {
	srv := newServer()

	// Attempt UPnP port mapping for port 8080 in a goroutine so it doesn't block startup
	go func() {
		log.Println("[*] Attempting UPnP port mapping for port 8080...")
		if MapPortUPnP(8080) {
			log.Println("[+] UPnP port mapping successful! Port 8080 is now open on your router.")
		} else {
			log.Println("[!] UPnP port mapping failed/unsupported. If connecting across the internet, please forward port 8080 manually.")
		}
	}()

	// Start local UDP discovery responder
	go startUDPResponder(srv)

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

// ──────────────────────────────────────────────
//  Dynamic Network and UPnP Helpers
// ──────────────────────────────────────────────

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func getPublicIP() string {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		resp, err = client.Get("https://icanhazip.com")
		if err != nil {
			return ""
		}
	}
	defer resp.Body.Close()
	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ipBytes))
}

// MapPortUPnP attempts to map the specified port using UPnP SSDP + SOAP.
func MapPortUPnP(port int) bool {
	ssdpMsg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 3\r\n\r\n"

	raddr, err := net.ResolveUDPAddr("udp", "239.255.255.250:1900")
	if err != nil {
		return false
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return false
	}
	defer conn.Close()

	_, err = conn.WriteTo([]byte(ssdpMsg), raddr)
	if err != nil {
		return false
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return false
	}

	resp := string(buf[:n])
	locReg := regexp.MustCompile(`(?i)LOCATION:\s*(http://[^\r\n]+)`)
	locMatch := locReg.FindStringSubmatch(resp)
	if len(locMatch) < 2 {
		return false
	}
	locationURL := locMatch[1]

	httpClient := http.Client{Timeout: 3 * time.Second}
	xmlResp, err := httpClient.Get(locationURL)
	if err != nil {
		return false
	}
	defer xmlResp.Body.Close()
	xmlData, err := io.ReadAll(xmlResp.Body)
	if err != nil {
		return false
	}

	controlReg := regexp.MustCompile(`<serviceType>urn:schemas-upnp-org:service:(WANIPConnection|WANPPPConnection):1</serviceType>[\s\S]*?<controlURL>([^<]+)</controlURL>`)
	controlMatch := controlReg.FindStringSubmatch(string(xmlData))
	if len(controlMatch) < 3 {
		return false
	}
	serviceType := "urn:schemas-upnp-org:service:" + controlMatch[1] + ":1"
	controlPath := controlMatch[2]

	baseURLReg := regexp.MustCompile(`(http://[^/]+)`)
	baseURLMatch := baseURLReg.FindStringSubmatch(locationURL)
	if len(baseURLMatch) < 2 {
		return false
	}
	baseURL := baseURLMatch[1]
	controlURL := baseURL + controlPath
	if !strings.HasPrefix(controlPath, "/") {
		controlURL = baseURL + "/" + controlPath
	}

	gatewayIPReg := regexp.MustCompile(`http://([^:]+)`)
	gatewayIPMatch := gatewayIPReg.FindStringSubmatch(baseURL)
	if len(gatewayIPMatch) < 2 {
		return false
	}
	gatewayIP := gatewayIPMatch[1]

	localIP := "127.0.0.1"
	dialConn, err := net.Dial("udp", gatewayIP+":80")
	if err == nil {
		localIP = dialConn.LocalAddr().(*net.UDPAddr).IP.String()
		dialConn.Close()
	}

	soapBody := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:AddPortMapping xmlns:u="` + serviceType + `">
  <NewRemoteHost></NewRemoteHost>
  <NewExternalPort>` + fmt.Sprintf("%d", port) + `</NewExternalPort>
  <NewProtocol>TCP</NewProtocol>
  <NewInternalPort>` + fmt.Sprintf("%d", port) + `</NewInternalPort>
  <NewInternalClient>` + localIP + `</NewInternalClient>
  <NewEnabled>1</NewEnabled>
  <NewPortMappingDescription>Ghost Chat</NewPortMappingDescription>
  <NewLeaseDuration>0</NewLeaseDuration>
</u:AddPortMapping>
</s:Body>
</s:Envelope>`

	req, err := http.NewRequest("POST", controlURL, bytes.NewBufferString(soapBody))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	req.Header.Set("SOAPAction", "\""+serviceType+"#AddPortMapping\"")

	soapResp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer soapResp.Body.Close()

	return soapResp.StatusCode == http.StatusOK
}
