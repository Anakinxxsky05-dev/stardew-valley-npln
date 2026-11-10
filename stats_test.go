package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
)

func TestStatsServerAuthorizationAndSchema(t *testing.T) {
	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "dash_token")

	// 1. Test token creation
	token, err := loadOrGenerateStatsToken(tokenFile)
	if err != nil {
		t.Fatalf("loadOrGenerateStatsToken failed: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(token))
	}

	// 2. Start server
	reg := newSessionRegistry()
	srv, err := startStatsServer("127.0.0.1:0", tokenFile, reg)
	if err != nil {
		t.Fatalf("startStatsServer failed: %v", err)
	}
	defer srv.Close()

	baseURL := "http://" + srv.listener.Addr().String()

	// 3. Test Unauthorized / Forbidden
	resp, err := http.Get(baseURL + "/api/stats")
	if err != nil {
		t.Fatalf("GET without token error: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden without token, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(baseURL + "/api/stats?key=invalid-token")
	if err != nil {
		t.Fatalf("GET with invalid token error: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden with invalid token, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. Test Method Not Allowed (POST)
	postResp, err := http.Post(baseURL+"/api/stats?key="+token, "application/json", nil)
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", postResp.StatusCode)
	}
	postResp.Body.Close()

	// 5. Test Authorized GET
	resp, err = http.Get(baseURL + "/api/stats?key=" + token)
	if err != nil {
		t.Fatalf("GET with valid token error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data statsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if data.Game != "0100e65002bb8000" {
		t.Errorf("expected game '0100e65002bb8000', got %q", data.Game)
	}
	if data.UptimeSeconds < 0 {
		t.Errorf("uptimeSeconds should be >= 0, got %d", data.UptimeSeconds)
	}
	if data.Server.Stack != "NPLN-Pia" {
		t.Errorf("expected stack 'NPLN-Pia', got %q", data.Server.Stack)
	}
}

func TestStatsSnapshotWithActivePlayersAndLobbies(t *testing.T) {
	reg := newSessionRegistry()
	globalStatsTracker.sessions = reg

	// Simulate connected player 1 (Host)
	uid1 := "u-host123"
	pid1 := uint64(1800001291)
	recordKnownUser(pid1, uid1, "0011223344556677", "hackerkin777")
	retenirIdentite("tenants/t-9f607adf-lp1/users/"+uid1, pid1)
	globalStatsTracker.userConnected(uid1, pid1)

	// Simulate connected player 2 (Guest)
	uid2 := "u-guest456"
	pid2 := uint64(1800003803)
	recordKnownUser(pid2, uid2, "7766554433221100", "alye")
	retenirIdentite("tenants/t-9f607adf-lp1/users/"+uid2, pid2)
	globalStatsTracker.userConnected(uid2, pid2)

	// Create active session in registry
	sessID := "gs-test-123"
	reg.sessions[sessID] = &mmpb.GameSession{
		Name:                "tenants/t-9f607adf-lp1/gameSessions/" + sessID,
		State:               mmpb.GameSession_ACTIVE,
		MaxParticipantCount: 4,
		UserSessions: []*mmpb.UserSession{
			{Name: "userSessions/us-1", User: "tenants/t-9f607adf-lp1/users/" + uid1, State: mmpb.UserSession_ACTIVE},
			{Name: "userSessions/us-2", User: "tenants/t-9f607adf-lp1/users/" + uid2, State: mmpb.UserSession_ACTIVE},
		},
	}
	reg.farmNames[sessID] = "Stardew Valley Farm"

	// Record method call
	globalStatsTracker.recordCall("GameSessionService/JoinGameSession", context.Background())

	snapshot := globalStatsTracker.buildSnapshot()

	if snapshot.Connected != 2 {
		t.Errorf("expected 2 connected players, got %d", snapshot.Connected)
	}
	if snapshot.InLobby != 2 {
		t.Errorf("expected 2 players in lobby, got %d", snapshot.InLobby)
	}
	if snapshot.ActiveLobbies != 1 {
		t.Errorf("expected 1 active lobby, got %d", snapshot.ActiveLobbies)
	}
	if len(snapshot.Gatherings) != 1 {
		t.Fatalf("expected 1 gathering, got %d", len(snapshot.Gatherings))
	}

	g := snapshot.Gatherings[0]
	if g.Type != "Stardew Valley Farm" {
		t.Errorf("expected gathering type 'Stardew Valley Farm', got %q", g.Type)
	}
	if g.Max != 4 {
		t.Errorf("expected max 4, got %d", g.Max)
	}
	if g.Count != 2 {
		t.Errorf("expected count 2, got %d", g.Count)
	}
	if len(g.Players) != 2 {
		t.Errorf("expected 2 gathering players, got %d", len(g.Players))
	}
	if !g.Players[0].Host {
		t.Errorf("expected first player to be host")
	}

	// Verify players idleSeconds and PID format
	for _, p := range snapshot.Players {
		if p.PID == 0 {
			t.Errorf("player %s has invalid PID 0", p.Name)
		}
		if p.IdleSeconds < 0 || p.IdleSeconds > 120 {
			t.Errorf("player %s has invalid idleSeconds: %d", p.Name, p.IdleSeconds)
		}
		if p.State != "lobby" {
			t.Errorf("expected state 'lobby', got %q", p.State)
		}
		if p.Activity != "Stardew Valley Farm" {
			t.Errorf("expected activity 'Stardew Valley Farm', got %q", p.Activity)
		}
	}

	// Disconnect player 2
	globalStatsTracker.userDisconnected(uid2)
	snap2 := globalStatsTracker.buildSnapshot()
	if snap2.Connected != 1 {
		t.Errorf("expected 1 connected player after disconnect, got %d", snap2.Connected)
	}
}
