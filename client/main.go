package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

// createRoom requests a new room from the relay server and returns the PIN.
func createRoom() (string, error) {
	resp, err := http.Post(serverAddr+"/room/create", "application/json", nil)
	if err != nil {
		return "", fmt.Errorf("POST /room/create: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return body.PIN, nil
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

	pin, err := createRoom()
	if err != nil {
		fmt.Printf("\n  %s Failed to create room: %v\n", red("✗"), err)
		promptContinue()
		return
	}

	aesKey := deriveKeyFromPIN(pin)

	fmt.Println()
	fmt.Println(bold("  ┌─── Room PIN ─────────────┐"))
	fmt.Printf("  │                          │\n")
	fmt.Printf("  │          %s          │\n", bold(brYellow(pin)))
	fmt.Printf("  │                          │\n")
	fmt.Println(bold("  └──────────────────────────┘"))
	fmt.Println()
	fmt.Println(dim("  Share this 6-digit PIN with your contact."))
	fmt.Println(dim("  The encryption key is derived locally — server never sees it."))

	enterRoom(cfg, pin, aesKey, serverAddr)
}

// ──────────────────────────────────────────────
//  Join Room Flow
// ──────────────────────────────────────────────

func flowJoin(cfg *Config) {
	fmt.Println()
	fmt.Print(bold("  Enter 6-digit PIN") + ": ")

	reader := bufio.NewReader(os.Stdin)
	raw, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("\n  %s Read error: %v\n", red("✗"), err)
		promptContinue()
		return
	}

	pin := strings.TrimSpace(raw)
	if err := validatePIN(pin); err != nil {
		fmt.Printf("\n  %s Invalid PIN: %v\n", red("✗"), err)
		promptContinue()
		return
	}

	fmt.Printf("\n  %s Searching local network for room %s...\n", dim("→"), pin)
	resolvedServerAddr, err := discoverLocalServer(pin)
	if err != nil {
		fmt.Printf("  %s Local discovery failed (timeout). Defaulting to localhost.\n", yellow("!"))
		resolvedServerAddr = "http://localhost:8080"
	} else {
		fmt.Printf("  %s Room found! Connecting to host at %s\n", green("✓"), resolvedServerAddr)
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

	// Hidden flag used by the installer to trigger first-run setup without
	// entering the main menu.
	setupMode := len(os.Args) > 1 && os.Args[1] == "--setup"

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
