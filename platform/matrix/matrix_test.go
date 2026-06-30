package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YingSuiAI/direxio-connect/core"
	"github.com/gorilla/websocket"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// --- Config validation (New) ---

func TestNew_MissingHomeserver(t *testing.T) {
	_, err := New(map[string]any{
		"access_token": "syt_test",
	})
	if err == nil || !strings.Contains(err.Error(), "homeserver is required") {
		t.Fatalf("expected homeserver error, got %v", err)
	}
}

func TestNew_MissingAccessToken(t *testing.T) {
	_, err := New(map[string]any{
		"homeserver": "https://matrix.org",
	})
	if err == nil || !strings.Contains(err.Error(), "access_token is required") {
		t.Fatalf("expected access_token error, got %v", err)
	}
}

func TestNew_ValidConfig(t *testing.T) {
	p, err := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "syt_test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plat := p.(*Platform)
	if plat.homeserver != "https://matrix.org" {
		t.Errorf("homeserver = %q, want https://matrix.org", plat.homeserver)
	}
	if plat.accessToken != "syt_test" {
		t.Errorf("accessToken = %q, want syt_test", plat.accessToken)
	}
	if plat.autoJoin != true {
		t.Error("autoJoin should default to true")
	}
}

func TestNew_AutoJoinDefault(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	plat := p.(*Platform)
	if !plat.autoJoin {
		t.Error("autoJoin should default to true when not specified")
	}
}

func TestNew_AutoJoinExplicitFalse(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
		"auto_join":    false,
	})
	plat := p.(*Platform)
	if plat.autoJoin {
		t.Error("autoJoin should be false when explicitly set to false")
	}
}

func TestNew_AutoVerifyDefault(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	plat := p.(*Platform)
	if !plat.autoVerify {
		t.Error("autoVerify should default to true when not specified")
	}
}

func TestNew_AutoVerifyExplicitFalse(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
		"auto_verify":  false,
	})
	plat := p.(*Platform)
	if plat.autoVerify {
		t.Error("autoVerify should be false when explicitly set to false")
	}
}

func TestNew_ProxyInvalidURL(t *testing.T) {
	_, err := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
		"proxy":        "://bad",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid proxy URL") {
		t.Fatalf("expected proxy error, got %v", err)
	}
}

func TestNew_ProxyValidURL(t *testing.T) {
	p, err := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
		"proxy":        "http://proxy:8080",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plat := p.(*Platform)
	if plat.proxyURL != "http://proxy:8080" {
		t.Errorf("proxyURL = %q", plat.proxyURL)
	}
}

func TestNew_AllOptions(t *testing.T) {
	p, err := New(map[string]any{
		"homeserver":               "https://matrix.org",
		"access_token":             "tok",
		"user_id":                  "@bot:matrix.org",
		"allow_from":               "@alice:matrix.org",
		"room_id":                  "!agent:matrix.org",
		"auto_join":                false,
		"share_session_in_channel": true,
		"group_reply_all":          true,
		"proxy":                    "socks5://proxy:1080",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plat := p.(*Platform)
	if plat.userID != "@bot:matrix.org" {
		t.Errorf("userID = %q", plat.userID)
	}
	if plat.allowFrom != "@alice:matrix.org" {
		t.Errorf("allowFrom = %q", plat.allowFrom)
	}
	if plat.allowedRoomID != "!agent:matrix.org" {
		t.Errorf("allowedRoomID = %q", plat.allowedRoomID)
	}
	if plat.shareSessionInChannel != true {
		t.Error("shareSessionInChannel should be true")
	}
	if plat.groupReplyAll != true {
		t.Error("groupReplyAll should be true")
	}
}

func TestNew_InvalidRoomID(t *testing.T) {
	_, err := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
		"room_id":      "not-a-room",
	})
	if err == nil || !strings.Contains(err.Error(), "room_id must be a Matrix room ID") {
		t.Fatalf("expected room_id error, got %v", err)
	}
}

// --- Name ---

func TestPlatform_Name(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	if p.Name() != "matrix" {
		t.Errorf("Name() = %q, want matrix", p.Name())
	}
}

// --- Helper functions ---

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		selfID string
		want   string
	}{
		{"empty selfID", "hello", "", "hello"},
		{"no mention", "hello world", "@bot:matrix.org", "hello world"},
		{"plain mention", "hello @bot:matrix.org how are you", "@bot:matrix.org", "hello  how are you"},
		{"matrix.to link", "https://matrix.to/#/@bot:matrix.org hello", "@bot:matrix.org", "hello"},
		{"mention at start", "@bot:matrix.org do something", "@bot:matrix.org", "do something"},
		{"mention at end", "do something @bot:matrix.org", "@bot:matrix.org", "do something"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.text, id.UserID(tt.selfID))
			if got != tt.want {
				t.Errorf("stripBotMention(%q, %q) = %q, want %q", tt.text, tt.selfID, got, tt.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		userID id.UserID
		want   string
	}{
		{"@alice:matrix.org", "alice"},
		{"@bob:synapse.example.com", "bob"},
		{"@user_name:server.org", "user_name"},
	}
	for _, tt := range tests {
		t.Run(string(tt.userID), func(t *testing.T) {
			got := displayName(tt.userID)
			if got != tt.want {
				t.Errorf("displayName(%q) = %q, want %q", tt.userID, got, tt.want)
			}
		})
	}
}

func TestBuildSessionKey(t *testing.T) {
	p := &Platform{}

	// Per-user (default)
	key := p.buildSessionKey("!room:server", "@user:server")
	want := "matrix:!room:server:@user:server"
	if key != want {
		t.Errorf("per-user key = %q, want %q", key, want)
	}

	// Shared session
	p.shareSessionInChannel = true
	key = p.buildSessionKey("!room:server", "@user:server")
	want = "matrix:!room:server"
	if key != want {
		t.Errorf("shared key = %q, want %q", key, want)
	}
}

func TestIsDirectedAtBot(t *testing.T) {
	p := &Platform{}
	selfID := id.UserID("@bot:matrix.org")

	tests := []struct {
		name    string
		content *event.MessageEventContent
		want    bool
	}{
		{
			"plain mention",
			&event.MessageEventContent{Body: "hey @bot:matrix.org help"},
			true,
		},
		{
			"matrix.to link",
			&event.MessageEventContent{
				Body:          "help",
				FormattedBody: `<a href="https://matrix.to/#/@bot:matrix.org">bot</a> help`,
			},
			true,
		},
		{
			"no mention",
			&event.MessageEventContent{Body: "just chatting"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.isDirectedAtBot(tt.content, selfID)
			if got != tt.want {
				t.Errorf("isDirectedAtBot() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- ReconstructReplyCtx ---

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid per-user", "matrix:!abc:server:@user:server", false},
		{"valid shared", "matrix:!abc:server", false},
		{"missing prefix", "telegram:!abc:server", true},
		{"too short", "matrix:", true},
		{"empty", "", true},
		{"invalid room ID", "matrix:notaroom", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rctx, err := p.ReconstructReplyCtx(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			rc, ok := rctx.(replyContext)
			if !ok {
				t.Fatal("expected replyContext type")
			}
			if tt.key == "matrix:!abc:server:@user:server" {
				if rc.roomID != "!abc:server" {
					t.Errorf("roomID = %q, want !abc:server", rc.roomID)
				}
			}
		})
	}
}

func TestReconstructReplyCtxRejectsDisallowedRoom(t *testing.T) {
	p := &Platform{allowedRoomID: "!allowed:server"}

	if _, err := p.ReconstructReplyCtx("matrix:!other:server"); err == nil {
		t.Fatal("expected disallowed room error")
	}
	rctx, err := p.ReconstructReplyCtx("matrix:!allowed:server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		t.Fatal("expected replyContext")
	}
	if rc.roomID != "!allowed:server" {
		t.Fatalf("roomID = %q, want !allowed:server", rc.roomID)
	}
}

// --- Reply/Send error paths ---

func TestReply_NotConnected(t *testing.T) {
	p := &Platform{}
	err := p.Reply(context.Background(), replyContext{roomID: "!room:s", messageID: "$evt"}, "hello")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not connected error, got %v", err)
	}
}

func TestReply_InvalidContext(t *testing.T) {
	p := &Platform{}
	err := p.Reply(context.Background(), "not-a-replyContext", "hello")
	if err == nil || !strings.Contains(err.Error(), "invalid reply context") {
		t.Fatalf("expected invalid context error, got %v", err)
	}
}

func TestSend_NotConnected(t *testing.T) {
	p := &Platform{}
	err := p.Send(context.Background(), replyContext{roomID: "!room:s", messageID: "$evt"}, "hello")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not connected error, got %v", err)
	}
}

func TestSendImage_NotConnected(t *testing.T) {
	p := &Platform{}
	err := p.SendImage(context.Background(), replyContext{roomID: "!room:s"}, core.ImageAttachment{Data: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not connected error, got %v", err)
	}
}

func TestSendFile_NotConnected(t *testing.T) {
	p := &Platform{}
	err := p.SendFile(context.Background(), replyContext{roomID: "!room:s"}, core.FileAttachment{Data: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not connected error, got %v", err)
	}
}

func TestUpdateMessage_NotConnected(t *testing.T) {
	p := &Platform{}
	err := p.UpdateMessage(context.Background(), replyContext{roomID: "!room:s", messageID: "$evt"}, "edited")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not connected error, got %v", err)
	}
}

func TestStartTyping_NotConnected(t *testing.T) {
	p := &Platform{}
	stop := p.StartTyping(context.Background(), replyContext{roomID: "!room:s"})
	stop()
}

func TestStartTyping_InvalidContext(t *testing.T) {
	p := &Platform{}
	stop := p.StartTyping(context.Background(), "bad")
	stop()
}

// --- Lifecycle: Start / Stop ---

func TestPlatform_StopWithoutStart(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	err := p.Stop()
	if err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}
}

func TestPlatform_StartStopIdempotent(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	plat := p.(*Platform)

	_ = plat.Start(func(_ core.Platform, _ *core.Message) {})
	_ = plat.Stop()
	_ = plat.Stop()

	err := plat.Start(func(_ core.Platform, _ *core.Message) {})
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("expected stopped error, got %v", err)
	}
}

func TestPlatform_StopPublishesAgentRoomOfflineStatus(t *testing.T) {
	statusEvents := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		wantPath := "/_matrix/client/v3/rooms/!room:matrix.org/state/io.direxio.agent.status/@agent:matrix.org"
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		statusEvents <- body
		_, _ = w.Write([]byte(`{"event_id":"$offline"}`))
	}))
	defer server.Close()

	client, err := mautrix.NewClient(server.URL, "", "tok")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.Client = server.Client()

	p := &Platform{
		client:        client,
		selfUserID:    "@agent:matrix.org",
		allowedRoomID: "!room:matrix.org",
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}

	select {
	case got := <-statusEvents:
		if got["online"] != false {
			t.Fatalf("online = %v, want false", got["online"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not publish offline status")
	}
}

func TestPlatform_RunConnectionPublishesAgentRoomOnlineStatus(t *testing.T) {
	statusEvents := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/_matrix/client/v3/account/whoami":
			_, _ = w.Write([]byte(`{"user_id":"@agent:matrix.org","device_id":"DEVICE"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/_matrix/client/v3/rooms/!room:matrix.org/state/io.direxio.agent.status/@agent:matrix.org":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			statusEvents <- body
			_, _ = w.Write([]byte(`{"event_id":"$online"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/_matrix/client/v3/sync":
			<-r.Context().Done()
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &Platform{
		homeserver:    server.URL,
		accessToken:   "tok",
		userID:        "@agent:matrix.org",
		allowedRoomID: "!room:matrix.org",
		httpClient:    server.Client(),
		dedup:         core.MessageDedup{},
	}
	done := make(chan error, 1)
	go func() {
		done <- p.runConnection(ctx)
	}()

	select {
	case got := <-statusEvents:
		if got["online"] != true {
			t.Fatalf("online = %v, want true", got["online"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runConnection() did not publish online status")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runConnection() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runConnection() did not stop after context cancellation")
	}
}

func TestPlatform_SetLifecycleHandler(t *testing.T) {
	p, _ := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	plat := p.(*Platform)

	plat.SetLifecycleHandler(&testLifecycleHandler{
		onUnavailable: func(_ core.Platform, _ error) {},
	})
	if plat.lifecycleHandler == nil {
		t.Error("lifecycleHandler should be set")
	}
}

// --- Dedup in handleMessage ---

func TestHandleMessage_SkipsOwnMessages(t *testing.T) {
	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var dispatched []*core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		dispatched = append(dispatched, msg)
	}

	evt := &event.Event{
		Sender: "@bot:matrix.org",
		ID:     "$own_msg",
		Type:   event.EventMessage,
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "my own message",
			},
		},
	}
	p.handleMessage(context.Background(), evt)

	if len(dispatched) != 0 {
		t.Error("should not dispatch own message")
	}
}

func TestHandleMessage_SkipsDuplicates(t *testing.T) {
	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var count int
	p.handler = func(_ core.Platform, msg *core.Message) {
		count++
	}

	evt := &event.Event{
		Sender:    "@user:matrix.org",
		ID:        "$dup_msg",
		Type:      event.EventMessage,
		Timestamp: time.Now().UnixMilli(),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello",
			},
		},
	}

	p.handleMessage(context.Background(), evt)
	p.handleMessage(context.Background(), evt)

	if count != 1 {
		t.Errorf("dispatched %d times, want 1", count)
	}
}

func TestHandleMessage_SkipsOldMessages(t *testing.T) {
	orig := core.StartTime
	core.StartTime = time.Now()
	defer func() { core.StartTime = orig }()

	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var count int
	p.handler = func(_ core.Platform, msg *core.Message) {
		count++
	}

	evt := &event.Event{
		Sender:    "@user:matrix.org",
		ID:        "$old_msg",
		Type:      event.EventMessage,
		Timestamp: time.Now().Add(-1 * time.Hour).UnixMilli(),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "old",
			},
		},
	}
	p.handleMessage(context.Background(), evt)

	if count != 0 {
		t.Error("should not dispatch old message")
	}
}

func TestHandleMessage_DispatchesText(t *testing.T) {
	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var received *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		received = msg
	}

	evt := &event.Event{
		RoomID:    "!room:server",
		Sender:    "@alice:matrix.org",
		ID:        "$text_msg",
		Type:      event.EventMessage,
		Timestamp: time.Now().UnixMilli(),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello bot",
			},
		},
	}
	p.handleMessage(context.Background(), evt)

	if received == nil {
		t.Fatal("expected message to be dispatched")
	}
	if received.Content != "hello bot" {
		t.Errorf("Content = %q, want hello bot", received.Content)
	}
	if received.UserID != "@alice:matrix.org" {
		t.Errorf("UserID = %q", received.UserID)
	}
	if received.UserName != "alice" {
		t.Errorf("UserName = %q", received.UserName)
	}
	if received.Platform != "matrix" {
		t.Errorf("Platform = %q", received.Platform)
	}
	if received.ChannelKey != "!room:server" {
		t.Errorf("ChannelKey = %q", received.ChannelKey)
	}
}

func TestHandleMessage_SkipsDisallowedRoom(t *testing.T) {
	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		allowedRoomID: "!agent:server",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var count int
	p.handler = func(_ core.Platform, msg *core.Message) {
		count++
	}

	evt := &event.Event{
		RoomID:    "!other:server",
		Sender:    "@alice:matrix.org",
		ID:        "$text_msg",
		Type:      event.EventMessage,
		Timestamp: time.Now().UnixMilli(),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello bot",
			},
		},
	}
	p.handleMessage(context.Background(), evt)

	if count != 0 {
		t.Fatalf("dispatched %d messages from a disallowed room", count)
	}
}

func TestHandleMessage_DispatchesAllowedRoom(t *testing.T) {
	p := &Platform{
		selfUserID:    "@bot:matrix.org",
		allowedRoomID: "!agent:server",
		dedup:         core.MessageDedup{},
		groupReplyAll: true,
	}

	var received *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		received = msg
	}

	evt := &event.Event{
		RoomID:    "!agent:server",
		Sender:    "@alice:matrix.org",
		ID:        "$text_msg",
		Type:      event.EventMessage,
		Timestamp: time.Now().UnixMilli(),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello agent",
			},
		},
	}
	p.handleMessage(context.Background(), evt)

	if received == nil {
		t.Fatal("expected message from allowed room")
	}
	if received.ChannelKey != "!agent:server" {
		t.Fatalf("ChannelKey = %q, want !agent:server", received.ChannelKey)
	}
}

func TestHandleMessage_NoticeAndEmote(t *testing.T) {
	for _, msgType := range []event.MessageType{event.MsgNotice, event.MsgEmote} {
		t.Run(string(msgType), func(t *testing.T) {
			p := &Platform{
				selfUserID:    "@bot:matrix.org",
				dedup:         core.MessageDedup{},
				groupReplyAll: true,
			}
			var received *core.Message
			p.handler = func(_ core.Platform, msg *core.Message) {
				received = msg
			}
			evt := &event.Event{
				RoomID:    "!room:s",
				Sender:    "@user:s",
				ID:        id.EventID("$" + string(msgType)),
				Type:      event.EventMessage,
				Timestamp: time.Now().UnixMilli(),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: msgType,
						Body:    "test",
					},
				},
			}
			p.handleMessage(context.Background(), evt)
			if received == nil {
				t.Fatalf("%s not dispatched", msgType)
			}
		})
	}
}

// --- handleMemberState (auto-join) ---

func TestHandleMemberState_AutoJoinDisabled(t *testing.T) {
	p := &Platform{
		autoJoin:   false,
		selfUserID: "@bot:matrix.org",
	}
	stateKey := "@bot:matrix.org"
	evt := &event.Event{
		RoomID:   "!room:s",
		StateKey: &stateKey,
		Content: event.Content{
			Parsed: &event.MemberEventContent{
				Membership: event.MembershipInvite,
			},
		},
	}
	p.handleMemberState(context.Background(), evt)
}

func TestHandleMemberState_NotForSelf(t *testing.T) {
	p := &Platform{
		autoJoin:   true,
		selfUserID: "@bot:matrix.org",
	}
	stateKey := "@other:matrix.org"
	evt := &event.Event{
		RoomID:   "!room:s",
		StateKey: &stateKey,
		Content: event.Content{
			Parsed: &event.MemberEventContent{
				Membership: event.MembershipInvite,
			},
		},
	}
	p.handleMemberState(context.Background(), evt)
}

// --- Concurrency-safe accessors ---

func TestConcurrentAccess(t *testing.T) {
	p := &Platform{}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.getClient()
			_ = p.getSelfUserID()
			_ = p.getHandler()
			_ = p.isStopping()
		}()
	}
	wg.Wait()
}

// --- Interface compliance ---

func TestInterfaceCompliance(t *testing.T) {
	p, err := New(map[string]any{
		"homeserver":   "https://matrix.org",
		"access_token": "tok",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	// Verify Platform interface is satisfied at compile time
	var _ core.Platform = (*Platform)(nil)

	if _, ok := p.(core.AsyncRecoverablePlatform); !ok {
		t.Error("should implement AsyncRecoverablePlatform")
	}
	if _, ok := p.(core.ReplyContextReconstructor); !ok {
		t.Error("should implement ReplyContextReconstructor")
	}
	if _, ok := p.(core.ImageSender); !ok {
		t.Error("should implement ImageSender")
	}
	if _, ok := p.(core.FileSender); !ok {
		t.Error("should implement FileSender")
	}
	if _, ok := p.(core.MessageUpdater); !ok {
		t.Error("should implement MessageUpdater")
	}
	if _, ok := p.(core.PreviewStarter); !ok {
		t.Error("should implement PreviewStarter")
	}
	if _, ok := p.(core.PreviewCleaner); !ok {
		t.Error("should implement PreviewCleaner")
	}
	if _, ok := p.(core.PreviewFinishPreference); !ok {
		t.Error("should implement PreviewFinishPreference")
	}
	if _, ok := p.(core.TypingIndicator); !ok {
		t.Error("should implement TypingIndicator")
	}
}

func TestDirexioAgentWSDispatchesAgentRoomMessage(t *testing.T) {
	done := make(chan struct{})
	asyncErr := make(chan error, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_p2p/command":
			if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
				asyncErr <- fmt.Errorf("authorization = %q", got)
			}
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				asyncErr <- err
			}
			if req["action"] != realtimeWSTicketAction {
				asyncErr <- fmt.Errorf("action = %v", req["action"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "ticket-1"})
		case "/_p2p/ws":
			if got := r.URL.Query().Get("ticket"); got != "ticket-1" {
				asyncErr <- fmt.Errorf("ticket = %q", got)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				asyncErr <- err
				return
			}
			defer conn.Close()
			var hello map[string]any
			if err := conn.ReadJSON(&hello); err != nil {
				asyncErr <- err
				return
			}
			if hello["type"] != "client.hello" {
				asyncErr <- fmt.Errorf("hello = %#v", hello)
			}
			_ = conn.WriteJSON(map[string]any{"type": "server.ready", "role": "agent"})
			_ = conn.WriteJSON(map[string]any{
				"type": "server.event",
				"event": map[string]any{
					"seq":      1,
					"type":     agentRoomMessageEventType,
					"room_id":  "!agent:example.com",
					"event_id": "$owner1",
					"payload": map[string]any{
						"room_id":          "!agent:example.com",
						"event_id":         "$owner1",
						"sender_mxid":      "@owner:example.com",
						"body":             "hello agent",
						"msgtype":          "m.text",
						"origin_server_ts": time.Now().UnixMilli(),
					},
				},
			})
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(done)

	p := &Platform{
		homeserver:    "http://matrix.example",
		accessToken:   "matrix-token",
		userID:        "@agent:example.com",
		selfUserID:    "@agent:example.com",
		allowedRoomID: "!agent:example.com",
		groupReplyAll: true,
		httpClient:    server.Client(),
		dedup:         core.MessageDedup{},
		p2pBaseURL:    server.URL + "/_p2p",
		p2pAgentToken: "agent-token",
	}
	got := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		got <- msg
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- p.runDirexioAgentWSOnce(ctx) }()

	select {
	case msg := <-got:
		if msg.Content != "hello agent" || msg.UserID != "@owner:example.com" || msg.ChannelKey != "!agent:example.com" {
			t.Fatalf("unexpected message: %#v", msg)
		}
		rc, ok := msg.ReplyCtx.(replyContext)
		if !ok || rc.roomID != "!agent:example.com" || rc.messageID != "$owner1" {
			t.Fatalf("unexpected reply context: %#v", msg.ReplyCtx)
		}
	case err := <-asyncErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket agent message")
	}
	cancel()
	select {
	case err := <-asyncErr:
		t.Fatal(err)
	default:
	}
	select {
	case <-runErr:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket loop to exit")
	}
}

func TestDirexioAgentStreamPreviewUsesWebSocketFrames(t *testing.T) {
	done := make(chan struct{})
	ready := make(chan struct{})
	frames := make(chan map[string]any, 4)
	asyncErr := make(chan error, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_p2p/command":
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "ticket-1"})
		case "/_p2p/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				asyncErr <- err
				return
			}
			defer conn.Close()
			var hello map[string]any
			if err := conn.ReadJSON(&hello); err != nil {
				asyncErr <- err
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "server.ready", "role": "agent"})
			close(ready)
			for {
				var frame map[string]any
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				select {
				case frames <- frame:
				case <-done:
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(done)

	p := &Platform{
		allowedRoomID: "!agent:example.com",
		httpClient:    server.Client(),
		p2pBaseURL:    server.URL + "/_p2p",
		p2pAgentToken: "agent-token",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- p.runDirexioAgentWSOnce(ctx) }()
	select {
	case <-ready:
	case err := <-asyncErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket ready")
	}

	handle, err := p.SendPreviewStart(ctx, replyContext{roomID: "!agent:example.com", messageID: "$owner1"}, "hello")
	if err != nil {
		t.Fatalf("SendPreviewStart() error: %v", err)
	}
	first := readDirexioStreamTestFrame(t, frames)
	if first["type"] != "client.agent_stream" || first["room_id"] != "!agent:example.com" || first["stream_id"] != "$owner1" || first["body"] != "hello" || first["replace"] != true {
		t.Fatalf("unexpected first stream frame: %#v", first)
	}

	if err := p.UpdateMessage(ctx, handle, "hello world"); err != nil {
		t.Fatalf("UpdateMessage() error: %v", err)
	}
	second := readDirexioStreamTestFrame(t, frames)
	if second["body"] != "hello world" || second["done"] == true {
		t.Fatalf("unexpected update stream frame: %#v", second)
	}

	if err := p.DeletePreviewMessage(ctx, handle); err != nil {
		t.Fatalf("DeletePreviewMessage() error: %v", err)
	}
	final := readDirexioStreamTestFrame(t, frames)
	if final["done"] != true || final["final_body"] != "hello world" || final["stream_id"] != "$owner1" {
		t.Fatalf("unexpected final stream frame: %#v", final)
	}
	if p.KeepPreviewOnFinish() {
		t.Fatal("direxio websocket previews should be cleaned before final Matrix reply")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket loop to exit")
	}
}

func TestDecorateOutgoingAgentMessageAddsGatewayStreamMetadata(t *testing.T) {
	p := &Platform{
		allowedRoomID: "!agent:example.com",
		p2pBaseURL:    "http://matrix.example/_p2p",
		p2pAgentToken: "agent-token",
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "final answer"}
	decorated, ok := p.decorateOutgoingAgentMessage(
		replyContext{roomID: "!agent:example.com", messageID: "$owner1"},
		content,
		"final answer",
	).(*agentGatewayMessageContent)
	if !ok {
		t.Fatalf("expected decorated message content, got %T", decorated)
	}
	if !decorated.AgentGateway || decorated.GatewaySource != agentGatewaySource {
		t.Fatalf("missing gateway metadata: %#v", decorated)
	}
	if decorated.AgentStream["stream_id"] != "$owner1" || decorated.AgentStream["final_body"] != "final answer" || decorated.AgentStream["done"] != true {
		t.Fatalf("unexpected stream metadata: %#v", decorated.AgentStream)
	}
}

// --- test helpers ---

func readDirexioStreamTestFrame(t *testing.T, frames <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket frame")
		return nil
	}
}

type testLifecycleHandler struct {
	onReady       func(core.Platform)
	onUnavailable func(core.Platform, error)
}

func (h testLifecycleHandler) OnPlatformReady(p core.Platform) {
	if h.onReady != nil {
		h.onReady(p)
	}
}

func (h testLifecycleHandler) OnPlatformUnavailable(p core.Platform, err error) {
	if h.onUnavailable != nil {
		h.onUnavailable(p, err)
	}
}
