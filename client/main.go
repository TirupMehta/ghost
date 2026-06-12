package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ──────────────────────────────────────────────
//  ANSI Terminal Colour Helpers
// ──────────────────────────────────────────────

const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiCyan      = "\033[36m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiRed       = "\033[31m"
	ansiBrWhite   = "\033[97m"
	ansiBrCyan    = "\033[96m"
	ansiBrGreen   = "\033[92m"
	ansiBrYellow  = "\033[93m"
	ansiBrMagenta = "\033[95m"
)

func bold(s string) string      { return ansiBold + s + ansiReset }
func cyan(s string) string      { return ansiCyan + s + ansiReset }
func green(s string) string     { return ansiGreen + s + ansiReset }
func yellow(s string) string    { return ansiYellow + s + ansiReset }
func red(s string) string       { return ansiRed + s + ansiReset }
func dim(s string) string       { return ansiDim + s + ansiReset }
func brCyan(s string) string    { return ansiBrCyan + s + ansiReset }
func brGreen(s string) string   { return ansiBrGreen + s + ansiReset }
func brYellow(s string) string  { return ansiBrYellow + s + ansiReset }
func brMagenta(s string) string { return ansiBrMagenta + s + ansiReset }

// clearScreen sends the ANSI clear + home sequence.
func clearScreen() { fmt.Print("\033[2J\033[H") }

// moveCursorUp moves the cursor up n lines and clears from cursor to end.
func moveCursorUp(n int) { fmt.Printf("\r\033[%dA\033[J", n) }

// printBanner renders the Ghost ASCII art header.
func printBanner() {
	clearScreen()
	fmt.Println()
	fmt.Println(bold(brCyan("  ██████╗ ██╗  ██╗ ██████╗ ███████╗████████╗")))
	fmt.Println(bold(brCyan("  ██╔════╝ ██║  ██║██╔═══██╗██╔════╝╚══██╔══╝")))
	fmt.Println(bold(brCyan("  ██║  ███╗███████║██║   ██║███████╗   ██║   ")))
	fmt.Println(bold(brCyan("  ██║   ██║██╔══██║██║   ██║╚════██║   ██║   ")))
	fmt.Println(bold(brCyan("  ╚██████╔╝██║  ██║╚██████╔╝███████║   ██║   ")))
	fmt.Println(bold(brCyan("   ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝   ╚═╝   ")))
	fmt.Println()
	fmt.Println(dim("  Ephemeral · Encrypted · Zero-Knowledge Chat"))
	fmt.Println(dim("  ─────────────────────────────────────────────"))
	fmt.Println()
}

// ──────────────────────────────────────────────
//  Config
// ──────────────────────────────────────────────

// Config is the persisted user configuration stored in ~/.ghost/config.json.
type Config struct {
	Handle string `json:"handle"`
}

// configPath returns the absolute path to the config file.
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".ghost", "config.json"), nil
}

// loadOrCreateConfig reads the config from disk or runs the first-run prompt.
func loadOrCreateConfig(setupMode bool) (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) || setupMode {
		return firstRunSetup(path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Handle == "" {
		return firstRunSetup(path)
	}

	return &cfg, nil
}

// firstRunSetup prompts the user for a handle and writes config.json.
func firstRunSetup(path string) (*Config, error) {
	fmt.Println(brYellow("  First run detected."))
	fmt.Println()
	fmt.Print(bold("  Enter your handle") + dim(" (no spaces, max 24 chars)") + ": ")

	reader := bufio.NewReader(os.Stdin)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading handle: %w", err)
	}

	handle := strings.TrimSpace(raw)
	handle = strings.ReplaceAll(handle, " ", "_")
	if len(handle) == 0 {
		handle = "ghost"
	}
	if len(handle) > 24 {
		handle = handle[:24]
	}

	cfg := &Config{Handle: handle}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, fmt.Errorf("writing config: %w", err)
	}

	fmt.Printf("\n  %s  Welcome, %s.\n\n", green("✓"), bold(brCyan(handle)))
	return cfg, nil
}

// ──────────────────────────────────────────────
//  Crypto
// ──────────────────────────────────────────────

// deriveKeyFromPIN deterministically produces a 32-byte AES-256 key from the
// 6-digit room PIN using SHA-256 with a fixed application salt.  The server
// never sees or computes this value — only clients who know the PIN can derive
// it.  The returned slice must be zeroed by the caller before going out of scope.
func deriveKeyFromPIN(pin string) []byte {
	h := sha256.Sum256([]byte("ghost-ephemeral-v1:" + pin))
	key := make([]byte, 32)
	copy(key, h[:])
	return key
}

// zeroKey overwrites every byte in key with 0x00 to prevent key material from
// lingering in process memory after the session ends.
func zeroKey(key []byte) {
	for i := range key {
		key[i] = 0x00
	}
}

// encryptMessage encrypts plaintext using AES-256-GCM with a freshly generated
// 12-byte nonce.  The output is nonce || ciphertext encoded as a hex string.
func encryptMessage(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(sealed), nil
}

// decryptMessage reverses encryptMessage.  Returns the plaintext string.
func decryptMessage(key []byte, ciphertextHex string) (string, error) {
	data, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", fmt.Errorf("hex decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, cipherBytes := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, cipherBytes, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open (wrong key?): %w", err)
	}

	return string(plain), nil
}

// ──────────────────────────────────────────────
//  Wire Types  (mirrors server/main.go)
// ──────────────────────────────────────────────

type Message struct {
	Timestamp   string `json:"ts"`
	Sender      string `json:"sender"`
	PayloadType string `json:"type"`
	Ciphertext  string `json:"ct"`
}

// ──────────────────────────────────────────────
//  Server Communication
// ──────────────────────────────────────────────

// serverAddr is the relay server base URL. Change this to point at your
// deployed instance before distributing the binary.
var serverAddr = "http://localhost:8080"

func init() {
	if envAddr := os.Getenv("GHOST_SERVER"); envAddr != "" {
		serverAddr = envAddr
	}
}

// createRoom requests a new room from the relay server and returns the PIN, IP, and port.
func createRoom() (string, string, string, error) {
	resp, err := http.Post(serverAddr+"/room/create", "application/json", nil)
	if err != nil {
		return "", "", "", fmt.Errorf("POST /room/create: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", "", "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body struct {
		PIN  string `json:"pin"`
		IP   string `json:"ip"`
		Port string `json:"port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", "", fmt.Errorf("decode response: %w", err)
	}

	return body.PIN, body.IP, body.Port, nil
}

// wsURL constructs the WebSocket URL for a given PIN.
func wsURL(pin string, addr string) string {
	u, _ := url.Parse(addr)
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/room/ws"
	q := u.Query()
	q.Set("pin", pin)
	u.RawQuery = q.Encode()
	return u.String()
}

// ──────────────────────────────────────────────
//  Chat Session
// ──────────────────────────────────────────────

// chatSession manages the WebSocket connection lifecycle for one active room.
type chatSession struct {
	handle     string
	pin        string
	aesKey     []byte // zeroed on Close
	serverAddr string

	mu   sync.Mutex
	ws   *websocket.Conn
	dead bool

	incoming chan Message
	outgoing chan string
	quit     chan struct{}
}

// newChatSession creates a session but does not open the WebSocket yet.
func newChatSession(handle, pin string, aesKey []byte, serverAddr string) *chatSession {
	return &chatSession{
		handle:     handle,
		pin:        pin,
		aesKey:     aesKey,
		serverAddr: serverAddr,
		incoming:   make(chan Message, 64),
		outgoing:   make(chan string, 64),
		quit:       make(chan struct{}),
	}
}

// connect opens the WebSocket connection to the relay server.
// Returns an error if the connection cannot be established.
func (s *chatSession) connect() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	ws, _, err := dialer.Dial(wsURL(s.pin, s.serverAddr), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	// Refresh read deadline on WebSocket-level pong frames (reply to our pings).
	ws.SetReadDeadline(time.Now().Add(120 * time.Second))
	ws.SetPongHandler(func(_ string) error {
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	// Refresh read deadline on WebSocket-level ping frames from the server and
	// send the required pong reply manually so gorilla doesn't swallow the frame.
	ws.SetPingHandler(func(data string) error {
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		return ws.WriteControl(
			websocket.PongMessage,
			[]byte(data),
			time.Now().Add(5*time.Second),
		)
	})

	s.mu.Lock()
	s.ws = ws
	s.dead = false
	s.mu.Unlock()

	return nil
}

// close tears down the WebSocket, sends a leave frame, and zeros the AES key.
func (s *chatSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dead {
		return
	}
	s.dead = true

	if s.ws != nil {
		leaveMsg := Message{
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			Sender:      s.handle,
			PayloadType: "leave",
		}
		_ = s.ws.WriteJSON(leaveMsg)
		_ = s.ws.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		s.ws.Close()
	}

	// Mandatory: overwrite the AES key bytes before the session is GC'd.
	zeroKey(s.aesKey)
}

// sendHandshake broadcasts a "handshake" join notification to the room.
// The handshake message content is encrypted just like a normal message so that
// the server never learns the plaintext.
func (s *chatSession) sendHandshake() error {
	ct, err := encryptMessage(s.aesKey, fmt.Sprintf("%s joined the room.", s.handle))
	if err != nil {
		return err
	}

	msg := Message{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Sender:      s.handle,
		PayloadType: "handshake",
		Ciphertext:  ct,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws.WriteJSON(msg)
}

// readLoop runs in a goroutine, continuously reading from the WebSocket and
// pushing decoded messages onto the incoming channel.
func (s *chatSession) readLoop() {
	defer func() {
		close(s.incoming)
	}()

	for {
		var msg Message
		s.mu.Lock()
		ws := s.ws
		s.mu.Unlock()

		if err := ws.ReadJSON(&msg); err != nil {
			select {
			case <-s.quit:
				return
			default:
			}
			if !websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway) {
				s.incoming <- Message{PayloadType: "error", Ciphertext: err.Error()}
			}
			return
		}

		// Refresh read deadline on every successfully received application message.
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))

		select {
		case s.incoming <- msg:
		case <-s.quit:
			return
		}
	}
}

// writeLoop runs in a goroutine, consuming plaintext strings from the outgoing
// channel, encrypting them, and sending them over the WebSocket.
func (s *chatSession) writeLoop() {
	// Client-side application-level ping ticker.
	pingTicker := time.NewTicker(25 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-s.quit:
			return

		case <-pingTicker.C:
			pingMsg := Message{
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				PayloadType: "ping",
			}
			s.mu.Lock()
			ws := s.ws
			isDead := s.dead
			s.mu.Unlock()
			if isDead {
				return
			}
			if err := ws.WriteJSON(pingMsg); err != nil {
				return
			}

		case text, ok := <-s.outgoing:
			if !ok {
				return
			}

			ct, err := encryptMessage(s.aesKey, text)
			if err != nil {
				fmt.Printf("\n%s encrypt error: %v\n", red("!"), err)
				continue
			}

			msg := Message{
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				Sender:      s.handle,
				PayloadType: "message",
				Ciphertext:  ct,
			}

			s.mu.Lock()
			ws := s.ws
			isDead := s.dead
			s.mu.Unlock()
			if isDead {
				return
			}
			if err := ws.WriteJSON(msg); err != nil {
				fmt.Printf("\n%s send error: %v\n", red("!"), err)
				return
			}
		}
	}
}

// formatTimestamp parses an RFC3339 string and returns HH:MM.
func formatTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "??:??"
	}
	return t.Local().Format("15:04")
}

// printMessage renders a decrypted chat message to stdout.
func printMessage(s *chatSession, msg Message) {
	if msg.Ciphertext == "" {
		return
	}

	plain, err := decryptMessage(s.aesKey, msg.Ciphertext)
	if err != nil {
		fmt.Printf("  %s [%s] %s: %s\n",
			dim(formatTimestamp(msg.Timestamp)),
			msg.Sender,
			red("⚠"),
			dim("(decryption failed – wrong key?)"),
		)
		return
	}

	ts := dim("[" + formatTimestamp(msg.Timestamp) + "]")
	var senderStr string

	switch msg.PayloadType {
	case "handshake":
		senderStr = brGreen("→ " + msg.Sender)
		fmt.Printf("  %s %s %s\n", ts, senderStr, dim(plain))
	case "message":
		senderStr = brMagenta(msg.Sender + ":")
		fmt.Printf("  %s %s %s\n", ts, senderStr, ansiBrWhite+plain+ansiReset)
	}
}

// runChatLoop drives the interactive chat TUI.
// Returns true if the session ended cleanly, false if a network error occurred.
func runChatLoop(s *chatSession) bool {
	go s.readLoop()
	go s.writeLoop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// eraseLine clears the current prompt line without touching anything above.
	eraseLine := func() { fmt.Print("\r\033[2K") }

	// Input reader: blocks on stdin, pushes complete lines into inputCh.
	inputCh := make(chan string, 8)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := reader.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if err != nil {
				close(inputCh)
				return
			}
			select {
			case inputCh <- line:
			case <-s.quit:
				return
			}
		}
	}()

	printPrompt(s.handle)

	for {
		select {
		case <-sigCh:
			fmt.Println()
			return true

		case msg, ok := <-s.incoming:
			if !ok {
				return false
			}
			if msg.PayloadType == "error" {
				eraseLine()
				fmt.Printf("  %s connection error: %s\n", red("✗"), msg.Ciphertext)
				return false
			}
			if msg.PayloadType == "pong" {
				continue
			}
			eraseLine()
			printMessage(s, msg)
			printPrompt(s.handle)

		case line, ok := <-inputCh:
			if !ok {
				return true
			}
			if line == "" {
				moveCursorUp(1)
				eraseLine()
				printPrompt(s.handle)
				continue
			}
			if strings.EqualFold(line, "/quit") || strings.EqualFold(line, "/exit") {
				moveCursorUp(1)
				eraseLine()
				return true
			}
			ts := dim("[" + time.Now().Local().Format("15:04") + "]")
			
			// Move up and clear the raw line the user typed, replacing it with the formatted local echo
			moveCursorUp(1)
			eraseLine()
			fmt.Printf("  %s %s %s\n", ts, cyan(s.handle+":"), ansiBrWhite+line+ansiReset)
			printPrompt(s.handle)

			select {
			case s.outgoing <- line:
			default:
				fmt.Printf("  %s outgoing buffer full\n", red("!"))
			}
		}
	}
}

// printPrompt renders the interactive input prompt line.
func printPrompt(handle string) {
	fmt.Printf("  %s %s ", bold(brCyan(handle)), bold(cyan("▶")))
}

// ──────────────────────────────────────────────
//  PIN Validation
// ──────────────────────────────────────────────

// validatePIN checks that the input is exactly 6 ASCII digits.
func validatePIN(pin string) error {
	if len(pin) != 6 {
		return fmt.Errorf("PIN must be exactly 6 digits (got %d characters)", len(pin))
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return fmt.Errorf("PIN must contain only digits")
		}
	}
	return nil
}

// discoverLocalServer sends a UDP broadcast to locate a Ghost relay server
// serving the specified PIN on the local network.
func discoverLocalServer(pin string) (string, error) {
	broadcastAddr, err := net.ResolveUDPAddr("udp", "255.255.255.255:9090")
	if err != nil {
		return "", fmt.Errorf("resolve broadcast address: %w", err)
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return "", fmt.Errorf("listen UDP: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2500 * time.Millisecond))

	stopBroadcast := make(chan struct{})
	go func() {
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		msg := []byte("GHOST_DISCOVER:" + pin)
		for {
			select {
			case <-ticker.C:
				_, _ = conn.WriteTo(msg, broadcastAddr)
			case <-stopBroadcast:
				return
			}
		}
	}()
	defer close(stopBroadcast)

	buf := make([]byte, 1024)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", fmt.Errorf("read response (timeout): %w", err)
		}

		reply := string(buf[:n])
		if reply == "GHOST_RESPONSE:"+pin {
			ip := remoteAddr.IP.String()
			return "http://" + ip + ":8080", nil
		}
	}
}

// ──────────────────────────────────────────────
//  Create Room Flow
// ──────────────────────────────────────────────

func flowCreate(cfg *Config) {
	fmt.Println()
	fmt.Printf("  %s Requesting new room from relay server...\n", dim("→"))

	pin, ip, port, err := createRoom()
	if err != nil {
		fmt.Printf("\n  %s Failed to create room: %v\n", red("✗"), err)
		promptContinue()
		return
	}

	rawToken := fmt.Sprintf("%s:%s:%s", ip, port, pin)
	token := "ghost://" + base64.StdEncoding.EncodeToString([]byte(rawToken))

	aesKey := deriveKeyFromPIN(pin)

	fmt.Println()
	fmt.Println(bold("  ┌─── Room PIN ─────────────┐"))
	fmt.Printf("  │                          │\n")
	fmt.Printf("  │          %s          │\n", bold(brYellow(pin)))
	fmt.Printf("  │                          │\n")
	fmt.Println(bold("  └──────────────────────────┘"))
	fmt.Println()
	fmt.Println(bold("  ┌─── Connection Token ────────────────────────────────────────┐"))
	fmt.Printf("  │  %s │\n", brCyan(token))
	fmt.Println(bold("  └─────────────────────────────────────────────────────────────┘"))
	fmt.Println()
	fmt.Println(dim("  Share the Room PIN (for local WiFi) or the Connection Token (for Internet)."))
	fmt.Println(dim("  All communication is 100% end-to-end encrypted directly between peers."))

	enterRoom(cfg, pin, aesKey, serverAddr)
}

// ──────────────────────────────────────────────
//  Join Room Flow
// ──────────────────────────────────────────────

func flowJoin(cfg *Config) {
	fmt.Println()
	fmt.Print(bold("  Enter 6-digit PIN or Connection Token") + ": ")

	reader := bufio.NewReader(os.Stdin)
	raw, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("\n  %s Read error: %v\n", red("✗"), err)
		promptContinue()
		return
	}

	input := strings.TrimSpace(raw)
	var pin, resolvedServerAddr string

	if strings.HasPrefix(input, "ghost://") {
		encoded := strings.TrimPrefix(input, "ghost://")
		decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			fmt.Printf("\n  %s Invalid Connection Token format: %v\n", red("✗"), err)
			promptContinue()
			return
		}
		parts := strings.Split(string(decodedBytes), ":")
		if len(parts) != 3 {
			fmt.Printf("\n  %s Invalid Connection Token content\n", red("✗"))
			promptContinue()
			return
		}
		ip, port, tokenPin := parts[0], parts[1], parts[2]
		pin = tokenPin
		resolvedServerAddr = "http://" + ip + ":" + port
		fmt.Printf("  %s Decoded token: connecting directly to %s\n", green("✓"), resolvedServerAddr)
	} else {
		pin = input
		if err := validatePIN(pin); err != nil {
			fmt.Printf("\n  %s Invalid PIN: %v\n", red("✗"), err)
			promptContinue()
			return
		}

		fmt.Printf("\n  %s Searching local network for room %s...\n", dim("→"), pin)
		resolvedAddr, err := discoverLocalServer(pin)
		if err != nil {
			fmt.Printf("  %s Local discovery failed (timeout). Defaulting to localhost.\n", yellow("!"))
			resolvedServerAddr = "http://localhost:8080"
		} else {
			resolvedServerAddr = resolvedAddr
			fmt.Printf("  %s Room found! Connecting to host at %s\n", green("✓"), resolvedServerAddr)
		}
	}

	aesKey := deriveKeyFromPIN(pin)
	enterRoom(cfg, pin, aesKey, resolvedServerAddr)
}

// ──────────────────────────────────────────────
//  Enter Room (shared by create & join)
// ──────────────────────────────────────────────

const maxRetries = 3

// enterRoom connects to the relay with retries and runs the interactive chat
// loop until the session ends or the retry budget is exhausted.
func enterRoom(cfg *Config, pin string, aesKey []byte, targetAddr string) {
	defer zeroKey(aesKey)

	session := newChatSession(cfg.Handle, pin, aesKey, targetAddr)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			wait := time.Duration(attempt) * 2 * time.Second
			fmt.Printf("\n  %s Reconnecting in %v (attempt %d/%d)...\n",
				yellow("↻"), wait, attempt, maxRetries)
			time.Sleep(wait)
		}

		if err := session.connect(); err != nil {
			fmt.Printf("  %s Connection failed: %v\n", red("✗"), err)
			if attempt == maxRetries {
				fmt.Printf("\n  %s Max retries reached. Returning to main menu.\n", red("✗"))
				return
			}
			continue
		}

		if err := session.sendHandshake(); err != nil {
			fmt.Printf("  %s Handshake failed: %v\n", red("✗"), err)
			session.close()
			continue
		}

		printChatHeader(pin)

		clean := runChatLoop(session)
		close(session.quit)
		session.close()

		if clean {
			printGoodbye()
			return
		}

		// Network error path: re-create the quit channel and try again.
		if attempt < maxRetries {
			session.quit = make(chan struct{})
			session.dead = false
		}
	}

	fmt.Printf("\n  %s Max retries reached. Returning to main menu.\n", red("✗"))
}

// ──────────────────────────────────────────────
//  TUI Helpers
// ──────────────────────────────────────────────

func printChatHeader(pin string) {
	fmt.Println()
	fmt.Println(dim("  ─────────────────────────────────────────────"))
	fmt.Printf("  %s  Room: %s  │  AES-256-GCM  │  /quit to exit\n",
		green("🔒"), bold(brYellow(pin)))
	fmt.Println(dim("  ─────────────────────────────────────────────"))
	fmt.Println()
}

func printGoodbye() {
	fmt.Println()
	fmt.Println(dim("  ─────────────────────────────────────────────"))
	fmt.Println(brCyan("  Session ended. Memory wiped."))
	fmt.Println(dim("  ─────────────────────────────────────────────"))
	fmt.Println()
}

func promptContinue() {
	fmt.Print(dim("  Press Enter to return to menu..."))
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ──────────────────────────────────────────────
//  Main Menu
// ──────────────────────────────────────────────

func mainMenu(cfg *Config) {
	for {
		printBanner()
		fmt.Printf("  Welcome back, %s\n\n", bold(brCyan(cfg.Handle)))
		fmt.Println(bold("  What would you like to do?"))
		fmt.Println()
		fmt.Printf("    %s  Create a new encrypted chat room\n", bold(brGreen("[1]")))
		fmt.Printf("    %s  Join an existing room with a token\n", bold(brYellow("[2]")))
		fmt.Printf("    %s  Change handle\n", bold(brMagenta("[3]")))
		fmt.Printf("    %s  Exit\n", bold(dim("[q]")))
		fmt.Println()
		fmt.Print(bold("  Choice") + ": ")

		reader := bufio.NewReader(os.Stdin)
		choice, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		choice = strings.TrimSpace(strings.ToLower(choice))

		switch choice {
		case "1":
			flowCreate(cfg)
		case "2":
			flowJoin(cfg)
		case "3":
			path, _ := configPath()
			newCfg, err := firstRunSetup(path)
			if err != nil {
				fmt.Printf("  %s Error: %v\n", red("✗"), err)
				promptContinue()
			} else {
				cfg.Handle = newCfg.Handle
			}
		case "q", "quit", "exit", "0":
			fmt.Println()
			fmt.Println(dim("  Goodbye."))
			fmt.Println()
			os.Exit(0)
		default:
			fmt.Printf("\n  %s Unknown option.\n", yellow("?"))
			time.Sleep(800 * time.Millisecond)
		}
	}
}

// ──────────────────────────────────────────────
//  Self Uninstall Flow
// ──────────────────────────────────────────────

func selfUninstall() {
	fmt.Println()
	fmt.Print(bold(red("  ⚠️  Are you sure you want to uninstall Ghost and delete all configuration? [y/N]")) + ": ")
	
	reader := bufio.NewReader(os.Stdin)
	ans, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	ans = strings.TrimSpace(strings.ToLower(ans))
	if ans != "y" && ans != "yes" {
		fmt.Println("  Uninstall aborted.")
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("  %s Error locating executable: %v\n", red("✗"), err)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  %s Error locating home directory: %v\n", red("✗"), err)
		return
	}
	configDir := filepath.Join(home, ".ghost")

	fmt.Println("  Uninstalling...")

	if runtime.GOOS == "windows" {
		// On Windows, the running exe is locked. We spawn a detached PowerShell process to clean it up after we exit.
		// Remove the config file first since it's not locked.
		configJson := filepath.Join(configDir, "config.json")
		_ = os.Remove(configJson)

		binDir := filepath.Join(configDir, "bin")
		cmd := fmt.Sprintf(
			"Start-Sleep -Seconds 1; " +
			"if (Test-Path '%s') { Remove-Item -Recurse -Force '%s' }; " +
			"$uPath = [Environment]::GetEnvironmentVariable('Path', 'User'); " +
			"if ($uPath -like '*%s*') { " +
			"  $paths = $uPath -split ';' | Where-Object { $_ -ne '%s' -and $_ -ne '' }; " +
			"  [Environment]::SetEnvironmentVariable('Path', ($paths -join ';'), 'User'); " +
			"}",
			configDir, configDir, binDir, binDir,
		)

		execCmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", cmd)
		if err := execCmd.Start(); err != nil {
			fmt.Printf("  %s Failed to spawn uninstaller: %v\n", red("✗"), err)
			return
		}

		fmt.Println(green("  ✓ Local config cleared. Terminal cleanup scheduled."))
		fmt.Println("  Goodbye.")
		os.Exit(0)
	} else {
		// Unix-like (macOS / Linux) allows unlinking the running binary.
		_ = os.Remove(exePath)
		_ = os.RemoveAll(configDir)

		fmt.Println(green("  ✓ Ghost CLI has been uninstalled and configuration wiped."))
		fmt.Println("  Goodbye.")
		os.Exit(0)
	}
}

// ──────────────────────────────────────────────
//  Entry Point
// ──────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "uninstall" || os.Args[1] == "--uninstall") {
		selfUninstall()
		return
	}

	setupMode := len(os.Args) > 1 && os.Args[1] == "--setup"

	// Start the background relay server inside the client process for normal runs
	if !setupMode {
		go startLocalServer()
		time.Sleep(50 * time.Millisecond) // Give the server a small headstart to bind
	}

	cfg, err := loadOrCreateConfig(setupMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghost: config error: %v\n", err)
		os.Exit(1)
	}

	if setupMode {
		fmt.Println(dim("  Setup complete. Run `ghost` to start."))
		fmt.Println()
		return
	}

	mainMenu(cfg)
}

// ──────────────────────────────────────────────
//  Embedded Relay Server
// ──────────────────────────────────────────────

type srvConn struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (c *srvConn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *srvConn) writePing() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.PingMessage, []byte("ghost-ping"))
}

type srvRoom struct {
	pin     string
	mu      sync.RWMutex
	conns   []*srvConn
	history []Message
}

const srvHistoryLimit = 100

func (r *srvRoom) addConn(c *srvConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns = append(r.conns, c)
}

func (r *srvRoom) removeConn(target *srvConn) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	updated := make([]*srvConn, 0, len(r.conns))
	for _, c := range r.conns {
		if c != target {
			updated = append(updated, c)
		}
	}
	r.conns = updated
	return len(r.conns)
}

func (r *srvRoom) appendHistory(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = append(r.history, m)
	if len(r.history) > srvHistoryLimit {
		r.history = r.history[len(r.history)-srvHistoryLimit:]
	}
}

func (r *srvRoom) fanOut(msg Message, sender *srvConn) {
	r.mu.RLock()
	targets := make([]*srvConn, len(r.conns))
	copy(targets, r.conns)
	r.mu.RUnlock()

	for _, c := range targets {
		if c == sender {
			continue
		}
		_ = c.writeJSON(msg)
	}
}

func (r *srvRoom) snapshotHistory() []Message {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap := make([]Message, len(r.history))
	copy(snap, r.history)
	return snap
}

type localServer struct {
	mu    sync.RWMutex
	rooms map[string]*srvRoom
}

func newLocalServer() *localServer {
	return &localServer{rooms: make(map[string]*srvRoom)}
}

func (s *localServer) createRoom() (string, error) {
	for {
		pin, err := srvRandomPIN()
		if err != nil {
			return "", err
		}

		s.mu.Lock()
		_, exists := s.rooms[pin]
		if !exists {
			s.rooms[pin] = &srvRoom{pin: pin}
			s.mu.Unlock()
			return pin, nil
		}
		s.mu.Unlock()
	}
}

func (s *localServer) getRoom(pin string) *srvRoom {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rooms[pin]
}

func (s *localServer) annihilate(pin string) {
	s.mu.Lock()
	delete(s.rooms, pin)
	s.mu.Unlock()
	runtime.GC()
}

func srvRandomPIN() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

var srvUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin:      func(_ *http.Request) bool { return true },
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
}

func (s *localServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pin, err := s.createRoom()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
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
}

func (s *localServer) handleWS(w http.ResponseWriter, r *http.Request) {
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

	wsConn, err := srvUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))
	wsConn.SetPongHandler(func(_ string) error {
		wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	peer := &srvConn{ws: wsConn}
	rm.addConn(peer)

	go func() {
		for _, msg := range rm.snapshotHistory() {
			if err := peer.writeJSON(msg); err != nil {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := peer.writePing(); err != nil {
					wsConn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	defer func() {
		close(done)
		wsConn.Close()
		remaining := rm.removeConn(peer)
		if remaining == 0 {
			s.annihilate(pin)
		}
	}()

	for {
		var msg Message
		if err := wsConn.ReadJSON(&msg); err != nil {
			return
		}

		wsConn.SetReadDeadline(time.Now().Add(90 * time.Second))

		if msg.PayloadType == "ping" {
			pong := Message{
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				PayloadType: "pong",
			}
			_ = peer.writeJSON(pong)
			continue
		}

		if msg.PayloadType == "leave" {
			return
		}

		if msg.PayloadType == "message" {
			rm.appendHistory(msg)
			rm.fanOut(msg, peer)
		}
	}
}

func startUDPResponder(srv *localServer) {
	addr, err := net.ResolveUDPAddr("udp", ":9090")
	if err != nil {
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
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

func startLocalServer() {
	srv := newLocalServer()

	// Attempt UPnP port mapping for port 8080 in a goroutine
	go func() {
		_ = MapPortUPnP(8080)
	}()

	// Start local UDP discovery responder
	go startUDPResponder(srv)

	mux := http.NewServeMux()
	mux.HandleFunc("/room/create", srv.handleCreate)
	mux.HandleFunc("/room/ws", srv.handleWS)

	httpSrv := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	_ = httpSrv.ListenAndServe()
}

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
