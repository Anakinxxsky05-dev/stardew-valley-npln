package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var serverStartTime = time.Now()

type statsPlayerInfo struct {
	UID         string
	PID         uint64
	Name        string
	ConnectedAt time.Time
	LastActive  time.Time
	LastAction  string
	Calls       int
}

type statsTracker struct {
	mu            sync.RWMutex
	activePlayers map[string]*statsPlayerInfo
	methodCalls   map[string]int
	totalCalls    int
	peakConnected int
	sessions      *sessionRegistry
}

var globalStatsTracker = &statsTracker{
	activePlayers: make(map[string]*statsPlayerInfo),
	methodCalls:   make(map[string]int),
}

func (t *statsTracker) recordCall(method string, ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cleanMethod := lastResourceSegment(method)
	t.methodCalls[cleanMethod]++
	t.totalCalls++

	uid := uidFromCtx(ctx)
	if uid == "" {
		return
	}

	p, exists := t.activePlayers[uid]
	if !exists {
		pid, _ := callerPID(ctx)
		if pid == 0 {
			pid = pidPourUid(uid)
		}
		if pid != 0 {
			retenirIdentite(uid, pid)
		}
		name := ""
		if pid != 0 {
			if u := getKnownUser(pid); u != nil && u.Name != "" {
				name = u.Name
			}
		}
		p = &statsPlayerInfo{
			UID:         uid,
			PID:         pid,
			Name:        name,
			ConnectedAt: time.Now(),
		}
		t.activePlayers[uid] = p
		if len(t.activePlayers) > t.peakConnected {
			t.peakConnected = len(t.activePlayers)
		}
	} else if p.PID == 0 {
		if pid, _ := callerPID(ctx); pid != 0 {
			p.PID = pid
			retenirIdentite(uid, pid)
		} else if pid := pidPourUid(uid); pid != 0 {
			p.PID = pid
		}
	}

	if p.Name == "" && p.PID != 0 {
		if u := getKnownUser(p.PID); u != nil && u.Name != "" {
			p.Name = u.Name
		}
	}

	p.LastActive = time.Now()
	p.LastAction = cleanMethod
	p.Calls++
}

func (t *statsTracker) userConnected(uid string, pid uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if uid == "" {
		return
	}
	if pid == 0 {
		pid = pidPourUid(uid)
	}
	name := ""
	if pid != 0 {
		if u := getKnownUser(pid); u != nil && u.Name != "" {
			name = u.Name
		}
	}
	p, exists := t.activePlayers[uid]
	if !exists {
		p = &statsPlayerInfo{
			UID:         uid,
			PID:         pid,
			Name:        name,
			ConnectedAt: time.Now(),
			LastActive:  time.Now(),
			LastAction:  "Connect",
			Calls:       1,
		}
		t.activePlayers[uid] = p
	} else {
		if pid != 0 {
			p.PID = pid
		}
		if name != "" {
			p.Name = name
		}
		p.LastActive = time.Now()
	}

	if len(t.activePlayers) > t.peakConnected {
		t.peakConnected = len(t.activePlayers)
	}
}

func (t *statsTracker) userDisconnected(uid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.activePlayers, uid)
}

func (t *statsTracker) userActive(uid string) {
	if uid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.activePlayers[uid]; ok {
		p.LastActive = time.Now()
	}
}

func (t *statsTracker) updatePlayerName(pid uint64, name string) {
	if pid == 0 || name == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.activePlayers {
		if p.PID == pid {
			p.Name = name
		}
	}
}

// Stats JSON models compatible with nextendo status_live.py and classics_presence
type statsResponse struct {
	Game          string           `json:"game"`
	Connected     int              `json:"connected"`
	InLobby       int              `json:"inLobby"`
	ActiveLobbies int              `json:"activeLobbies"`
	PeakConnected int              `json:"peakConnected"`
	TotalRMC      int              `json:"totalRmc"`
	UptimeSeconds int              `json:"uptimeSeconds"`
	Server        statsServerInfo  `json:"server"`
	Players       []statsPlayer    `json:"players"`
	Gatherings    []statsGathering `json:"gatherings"`
	Methods       []statsMethod    `json:"methods"`
}

type statsServerInfo struct {
	SNIHost    string `json:"sniHost"`
	SecurePort int    `json:"securePort"`
	Stack      string `json:"stack"`
}

type statsPlayer struct {
	PID           uint64 `json:"pid"`
	Name          string `json:"name"`
	State         string `json:"state"`
	Activity      string `json:"activity"`
	Gathering     int    `json:"gathering"`
	LastAction    string `json:"lastAction"`
	Calls         int    `json:"calls"`
	OnlineSeconds int    `json:"onlineSeconds"`
	IdleSeconds   int    `json:"idleSeconds"`
}

type statsGathering struct {
	ID      int                    `json:"id"`
	Type    string                 `json:"type"`
	Kind    string                 `json:"kind"`
	Count   int                    `json:"count"`
	Max     int                    `json:"max"`
	State   string                 `json:"state"`
	Players []statsGatheringPlayer `json:"players"`
}

type statsGatheringPlayer struct {
	PID  uint64 `json:"pid"`
	Name string `json:"name"`
	Host bool   `json:"host"`
}

type statsMethod struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (t *statsTracker) buildSnapshot() *statsResponse {
	// 1. Snapshot sessions outside tracker mutex to eliminate lock inversion/deadlocks
	var activeSessions []statsSessionInfo
	if t.sessions != nil {
		activeSessions = t.sessions.snapshotForStats()
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	resp := &statsResponse{
		Game:          strings.ToLower(nplnAppID),
		PeakConnected: t.peakConnected,
		TotalRMC:      t.totalCalls,
		UptimeSeconds: int(now.Sub(serverStartTime).Seconds()),
		Server: statsServerInfo{
			SNIHost:    stardewTLSHostname,
			SecurePort: 443,
			Stack:      "NPLN-Pia",
		},
		Players:       make([]statsPlayer, 0),
		Gatherings:    make([]statsGathering, 0),
		Methods:       make([]statsMethod, 0),
		ActiveLobbies: len(activeSessions),
	}

	// Format gatherings
	for _, s := range activeSessions {
		state := "open"
		if len(s.members) >= s.maxPlayers {
			state = "full"
		}
		resp.Gatherings = append(resp.Gatherings, statsGathering{
			ID:      s.id,
			Type:    s.worldName,
			Kind:    "online",
			Count:   len(s.members),
			Max:     s.maxPlayers,
			State:   state,
			Players: s.members,
		})
	}

	// 2. Active players
	inLobbyCount := 0
	for uid, p := range t.activePlayers {
		pid := p.PID
		if pid == 0 {
			pid = pidPourUid(uid)
		}
		if pid == 0 {
			continue // PID must be positive integer for status_live.py
		}

		name := p.Name
		if name == "" {
			if u := getKnownUser(pid); u != nil && u.Name != "" {
				name = u.Name
			} else {
				name = fmt.Sprintf("Player %d", pid)
			}
		}

		// Determine lobby membership
		lobbyID := 0
		activity := "Main Menu"
		state := "online"

		for _, s := range activeSessions {
			if s.memberUIDs[uid] {
				lobbyID = s.id
				activity = s.worldName
				state = "lobby"
				inLobbyCount++
				break
			}
		}

		idleSec := int(now.Sub(p.LastActive).Seconds())
		if idleSec < 0 {
			idleSec = 0
		}
		// If player is actively in a lobby, they are active in the session;
		// keep idleSec within the active window (max 30s) so status monitors don't drop them
		if state == "lobby" && idleSec > 30 {
			idleSec = 30
		}
		onlineSec := int(now.Sub(p.ConnectedAt).Seconds())
		if onlineSec < 0 {
			onlineSec = 0
		}

		resp.Players = append(resp.Players, statsPlayer{
			PID:           pid,
			Name:          name,
			State:         state,
			Activity:      activity,
			Gathering:     lobbyID,
			LastAction:    p.LastAction,
			Calls:         p.Calls,
			OnlineSeconds: onlineSec,
			IdleSeconds:   idleSec,
		})
	}

	resp.Connected = len(resp.Players)
	resp.InLobby = inLobbyCount
	if resp.Connected > resp.PeakConnected {
		resp.PeakConnected = resp.Connected
		t.peakConnected = resp.Connected
	}

	// 3. Methods
	for k, v := range t.methodCalls {
		resp.Methods = append(resp.Methods, statsMethod{
			Name:  k,
			Count: v,
		})
	}
	sort.Slice(resp.Methods, func(i, j int) bool {
		return resp.Methods[i].Count > resp.Methods[j].Count
	})

	return resp
}

type statsServer struct {
	httpServer *http.Server
	listener   net.Listener
	token      string
}

func (s *statsServer) Close() error {
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}

func loadOrGenerateStatsToken(path string) (string, error) {
	if envTok := os.Getenv("NPLN_STATS_TOKEN"); envTok != "" {
		return strings.TrimSpace(envTok), nil
	}

	if data, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(data))
		if len(tok) >= 16 {
			return tok, nil
		}
	}

	// Generate 32 cryptographically secure random bytes (64 hex characters)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate stats token: %w", err)
	}
	tok := hex.EncodeToString(buf)

	// Ensure parent directory exists
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}

	if err := os.WriteFile(path, []byte(tok+"\n"), 0600); err != nil {
		log.Printf("[NPLN Stats] Warning: could not write stats token to %s: %v", path, err)
	} else {
		log.Printf("[NPLN Stats] Generated and saved new stats token to %s (mode 0600)", path)
	}

	return tok, nil
}

func startStatsServer(listenAddr, tokenFile string, reg *sessionRegistry) (*statsServer, error) {
	if tokenFile == "" {
		tokenFile = envOr("NPLN_STATS_TOKEN_FILE", "dash_token")
	}
	token, err := loadOrGenerateStatsToken(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("stats token initialization: %w", err)
	}

	globalStatsTracker.sessions = reg

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		key := r.URL.Query().Get("key")
		if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(token)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		snapshot := globalStatsTracker.buildSnapshot()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(snapshot)
	})

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen stats on %s: %w", listenAddr, err)
	}

	srv := &http.Server{
		Handler:      http.TimeoutHandler(mux, 4*time.Second, "{\"error\":\"timeout\"}\n"),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("[NPLN Stats] Listening on http://%s/api/stats (token=%s...)", lis.Addr(), token[:8])
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Printf("[NPLN Stats] HTTP server error: %v", err)
		}
	}()

	return &statsServer{
		httpServer: srv,
		listener:   lis,
		token:      token,
	}, nil
}
