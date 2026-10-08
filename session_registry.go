package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
)

// One registry is injected into all three services by buildServer. Session
// pointers and their membership are accessed only while mu is held.
type sessionRegistry struct {
	mu         sync.Mutex
	sessions   map[string]*mmpb.GameSession
	pooled     map[string]*publicMatchSession
	friendUIDs func(context.Context, string) (map[string]bool, error)
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[string]*mmpb.GameSession), pooled: make(map[string]*publicMatchSession), friendUIDs: canonicalFriendUIDs}
}

func chooseRegistry(registries []*sessionRegistry) *sessionRegistry {
	if len(registries) > 0 && registries[0] != nil {
		return registries[0]
	}
	return newSessionRegistry()
}

// Metadata uid is a routing hint, not proof of identity. Require the signed,
// unexpired tenant access token at matchmaking RPC boundaries.
func authenticatedNPLNUID(ctx context.Context) (string, error) {
	token, err := bearerToken(ctx)
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "NPLN bearer token required")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || !verifyNplnAccessToken(parts[0], parts[1], parts[2]) {
		return "", status.Error(codes.Unauthenticated, "invalid token signature")
	}
	var claims struct {
		Subject string `json:"sub"`
		Issuer  string `json:"iss"`
		Expires int64  `json:"exp"`
		NPLN    struct {
			Tenant string `json:"tid"`
		} `json:"npln"`
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil || claims.Subject == "" || claims.Issuer != nplnIssuer || claims.Expires <= time.Now().Unix() || !matchTenantID(claims.NPLN.Tenant) {
		return "", status.Error(codes.Unauthenticated, "invalid or expired tenant access token")
	}
	if hinted := uidFromCtx(ctx); hinted != "" && hinted != claims.Subject {
		return "", status.Error(codes.PermissionDenied, "uid metadata does not match bearer subject")
	}
	return claims.Subject, nil
}

func canonicalFriendUIDs(ctx context.Context, uid string) (map[string]bool, error) {
	friends := map[string]bool{uid: true}
	pid, ok := callerPID(ctx)
	if !ok {
		if envEnabled("NPLN_ALLOW_UNVERIFIED") {
			for _, o := range getOtherKnownUsers(0) {
				friends[o.UserID] = true
			}
			return friends, nil
		}
		return nil, status.Error(codes.Unauthenticated, "account binding required")
	}
	account, err := accountFriends(pid)
	if err != nil {
		if envEnabled("NPLN_ALLOW_UNVERIFIED") {
			for _, o := range getOtherKnownUsers(pid) {
				friends[o.UserID] = true
			}
			return friends, nil
		}
		return nil, status.Error(codes.Unavailable, "friend service unavailable")
	}
	if account != nil {
		for _, friend := range account.Friends {
			friends[friend.UserID] = true
		}
	}
	if envEnabled("NPLN_ALLOW_UNVERIFIED") {
		for _, o := range getOtherKnownUsers(pid) {
			friends[o.UserID] = true
		}
	}
	return friends, nil
}

func sessionHasUID(session *mmpb.GameSession, uid string) bool {
	for _, u := range session.GetUserSessions() {
		if userIDFromPath(u.GetUser()) == uid && u.GetState() == mmpb.UserSession_ACTIVE {
			return true
		}
	}
	return false
}

func (r *sessionRegistry) member(gameSession, userSession, uid string) (*mmpb.GameSession, *mmpb.UserSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gs := r.sessions[lastResourceSegment(gameSession)]
	for _, u := range gs.GetUserSessions() {
		if lastResourceSegment(u.Name) == lastResourceSegment(userSession) && userIDFromPath(u.User) == uid && u.State == mmpb.UserSession_ACTIVE {
			return proto.Clone(gs).(*mmpb.GameSession), proto.Clone(u).(*mmpb.UserSession)
		}
	}
	return nil, nil
}

func (r *sessionRegistry) depart(gameSession, userSession string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := lastResourceSegment(gameSession)
	gs := r.sessions[id]
	if gs == nil {
		return
	}
	users := gs.UserSessions[:0]
	for _, u := range gs.UserSessions {
		if lastResourceSegment(u.Name) != lastResourceSegment(userSession) {
			users = append(users, u)
		}
	}
	gs.UserSessions = users
	gs.CurrentParticipantCount = int32(len(users))
	if pooled := r.pooled[id]; pooled != nil {
		members := pooled.members[:0]
		for _, u := range pooled.members {
			if lastResourceSegment(u.userSession) != lastResourceSegment(userSession) {
				members = append(members, u)
			}
		}
		pooled.members = members
	}
	// Keep the object as a tombstone for old tickets; it is never selectable.
	if len(users) == 0 {
		gs.CanParticipate = false
		gs.State = mmpb.GameSession_TERMINATED
	}
}

func (r *sessionRegistry) updateSessionFromGamesync(gameSession string, doc *gspb.Document) {
	if r == nil || doc == nil || doc.Fields == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	id := lastResourceSegment(gameSession)
	gs := r.sessions[id]
	if gs == nil {
		log.Printf("[NPLN SessionRegistry] updateSessionFromGamesync: session %q not found in registry", id)
		return
	}

	fields := doc.Fields.GetFields()
	if ipVal := fields["ip"]; ipVal != nil {
		if b, ok := ipVal.GetValueType().(*commonpb.Value_BooleanValue); ok {
			gs.IsPublic = b.BooleanValue
		}
	}
	if cpVal := fields["cp"]; cpVal != nil {
		if b, ok := cpVal.GetValueType().(*commonpb.Value_BooleanValue); ok {
			gs.CanParticipate = b.BooleanValue
		}
	}
	if pwdVal := fields["pwd"]; pwdVal != nil {
		if s, ok := pwdVal.GetValueType().(*commonpb.Value_StringValue); ok {
			gs.Password = s.StringValue
		}
	}
	if maxuVal := fields["maxu"]; maxuVal != nil {
		if i, ok := maxuVal.GetValueType().(*commonpb.Value_IntegerValue); ok {
			gs.MaxParticipantCount = int32(i.IntegerValue)
		}
	}

	if prpVal := fields["prp"]; prpVal != nil {
		if prpMap := prpVal.GetMapValue(); prpMap != nil {
			if gs.Properties == nil {
				gs.Properties = &commonpb.MapValue{Fields: make(map[string]*commonpb.Value)}
			} else if gs.Properties.Fields == nil {
				gs.Properties.Fields = make(map[string]*commonpb.Value)
			}
			for k, v := range prpMap.GetFields() {
				if v != nil {
					gs.Properties.Fields[k] = proto.Clone(v).(*commonpb.Value)
				}
			}
			if piaVal := gs.Properties.Fields["_Pia_SystemData"]; piaVal != nil {
				if b := piaVal.GetBytesValue(); len(b) > 28 {
					nameLen := int(b[26])
					if nameLen > 0 && 28+nameLen <= len(b) {
						name := string(b[28 : 28+nameLen])
						if len(gs.UserSessions) > 0 {
							hostUID := lastResourceSegment(gs.UserSessions[0].User)
							if hostPID := pidPourUid(hostUID); hostPID != 0 {
								updateKnownUserName(hostPID, name)
							}
						}
					}
				}
			}
			log.Printf("[NPLN SessionRegistry] Updated properties for session %s (total properties: %d)", id, len(gs.Properties.Fields))
		}
	}
}
