package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/YingSuiAI/direxio-connect/core"
	"github.com/gorilla/websocket"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

func init() {
	core.RegisterPlatform("matrix", New)
}

type replyContext struct {
	roomID    id.RoomID
	messageID id.EventID
}

type agentStreamPreviewHandle struct {
	roomID   id.RoomID
	streamID string

	mu       sync.Mutex
	lastBody string
}

type agentGatewayMessageContent struct {
	*event.MessageEventContent

	AgentGateway  bool           `json:"io.direxio.agent_gateway,omitempty"`
	GatewaySource string         `json:"io.direxio.gateway_source,omitempty"`
	AgentStream   map[string]any `json:"io.direxio.agent_stream,omitempty"`
}

type Platform struct {
	homeserver            string
	accessToken           string
	userID                string
	allowFrom             string
	allowedRoomID         id.RoomID
	shareSessionInChannel bool
	groupReplyAll         bool
	autoJoin              bool
	autoVerify            bool
	proxyURL              string

	mu                   sync.RWMutex
	client               *mautrix.Client
	selfUserID           id.UserID
	handler              core.MessageHandler
	lifecycleHandler     core.PlatformLifecycleHandler
	cancel               context.CancelFunc
	stopping             bool
	generation           uint64
	everConnected        bool
	unavailableNotified  bool
	dedup                core.MessageDedup
	httpClient           *http.Client
	cryptoHelper         any //nolint:unused // *cryptohelper.CryptoHelper when built with goolm tag
	crossSigningPassword string

	p2pBaseURL    string
	p2pAgentToken string
	p2pMu         sync.Mutex
	p2pWriteMu    sync.Mutex
	p2pConn       *websocket.Conn
	p2pStreamSeq  int64
}

const (
	agentRoomStatusEventType  = "io.direxio.agent.status"
	agentRoomMessageEventType = "agent_room.message"
	agentGatewaySource        = "direxio-connect"
	realtimeWSTicketAction    = "realtime.ws_ticket.create"
	initialBackoff            = 2 * time.Second
	maxBackoff                = 60 * time.Second
	stableWindow              = 10 * time.Second
)

var agentRoomStatusMatrixEventType = event.Type{Type: agentRoomStatusEventType, Class: event.StateEventType}

func New(opts map[string]any) (core.Platform, error) {
	homeserver, _ := opts["homeserver"].(string)
	if homeserver == "" {
		return nil, fmt.Errorf("matrix: homeserver is required")
	}
	accessToken, _ := opts["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("matrix: access_token is required")
	}
	userID, _ := opts["user_id"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("matrix", allowFrom)
	allowedRoomID, _ := opts["room_id"].(string)
	if allowedRoomID == "" {
		allowedRoomID, _ = opts["allowed_room_id"].(string)
	}
	if allowedRoomID != "" && !strings.HasPrefix(allowedRoomID, "!") {
		return nil, fmt.Errorf("matrix: room_id must be a Matrix room ID")
	}

	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSession, _ := opts["share_session_in_channel"].(bool)
	autoJoin, _ := opts["auto_join"].(bool)
	if !autoJoin {
		_, hasKey := opts["auto_join"]
		if !hasKey {
			autoJoin = true // default true
		}
	}
	autoVerify, _ := opts["auto_verify"].(bool)
	if !autoVerify {
		_, hasKey := opts["auto_verify"]
		if !hasKey {
			autoVerify = true // default true
		}
	}
	proxyURL, _ := opts["proxy"].(string)
	crossSigningPassword, _ := opts["cross_signing_password"].(string)
	if env := os.Getenv("MATRIX_CROSS_SIGNING_PASSWORD"); env != "" {
		crossSigningPassword = env
	}
	p2pBaseURL, _ := opts["p2p_base_url"].(string)
	if p2pBaseURL == "" {
		p2pBaseURL, _ = opts["direxio_p2p_base_url"].(string)
	}
	p2pAgentToken, _ := opts["agent_token"].(string)
	if p2pAgentToken == "" {
		p2pAgentToken, _ = opts["p2p_agent_token"].(string)
	}
	if env := os.Getenv("DIREXIO_P2P_BASE_URL"); env != "" {
		p2pBaseURL = env
	}
	if env := os.Getenv("DIREXIO_AGENT_TOKEN"); env != "" {
		p2pAgentToken = env
	}
	if p2pAgentToken != "" && p2pBaseURL == "" {
		p2pBaseURL = deriveP2PBaseURL(homeserver)
	}
	p2pBaseURL = strings.TrimRight(strings.TrimSpace(p2pBaseURL), "/")
	p2pAgentToken = strings.TrimSpace(p2pAgentToken)
	if p2pBaseURL != "" {
		u, err := url.Parse(p2pBaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("matrix: invalid p2p_base_url %q", p2pBaseURL)
		}
	}

	httpClient := &http.Client{Timeout: 120 * time.Second}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("matrix: invalid proxy URL %q: %w", proxyURL, err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("matrix: using proxy", "proxy", u.Host)
	}

	return &Platform{
		homeserver:            homeserver,
		accessToken:           accessToken,
		userID:                userID,
		allowFrom:             allowFrom,
		allowedRoomID:         id.RoomID(allowedRoomID),
		groupReplyAll:         groupReplyAll,
		shareSessionInChannel: shareSession,
		autoJoin:              autoJoin,
		proxyURL:              proxyURL,
		autoVerify:            autoVerify,
		crossSigningPassword:  crossSigningPassword,
		httpClient:            httpClient,
		dedup:                 core.MessageDedup{},
		p2pBaseURL:            p2pBaseURL,
		p2pAgentToken:         p2pAgentToken,
	}, nil
}

func (p *Platform) Name() string { return "matrix" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return fmt.Errorf("matrix: platform stopped")
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.handler = handler
	p.cancel = cancel

	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialBackoff

	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		startedAt := time.Now()
		err := p.runConnection(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		wait := backoff
		if time.Since(startedAt) >= stableWindow {
			wait = initialBackoff
			backoff = initialBackoff
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		if err != nil {
			slog.Warn("matrix: connection error, retrying", "error", core.RedactToken(err.Error(), p.accessToken), "backoff", wait)
			p.notifyUnavailable(err)
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Platform) runConnection(ctx context.Context) error {
	client, err := mautrix.NewClient(p.homeserver, "", p.accessToken)
	if err != nil {
		return fmt.Errorf("matrix: create client: %w", err)
	}
	client.Client = p.httpClient

	// Always call Whoami to validate token and get device ID (needed for E2EE)
	selfUserID := id.UserID(p.userID)
	var deviceID id.DeviceID
	resp, err := client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("matrix: whoami: %w", err)
	}
	if selfUserID == "" {
		selfUserID = resp.UserID
	}
	deviceID = resp.DeviceID
	client.UserID = selfUserID
	client.DeviceID = deviceID

	if ctx.Err() != nil || p.isStopping() {
		return nil
	}

	gen, ok := p.publishClient(client, selfUserID)
	if !ok {
		return nil
	}

	// Initialize E2EE crypto helper
	p.initE2EE(ctx, client)

	if err := p.publishAgentRoomStatus(ctx, true); err != nil {
		slog.Warn("matrix: publish agent online status failed", "error", core.RedactToken(err.Error(), p.accessToken))
	}
	wsCtx, cancelWS := context.WithCancel(ctx)
	defer cancelWS()
	if p.direxioWSConfigured() {
		go p.runDirexioAgentWSLoop(wsCtx)
	}

	slog.Info("matrix: connected", "user_id", selfUserID)
	p.emitReady(gen)

	// Register event handlers.
	// Note: EventEncrypted is handled by cryptohelper which decrypts and
	// re-dispatches as EventMessage, so we only need EventMessage here.
	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		p.handleMessage(ctx, evt)
	})
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		p.handleMemberState(ctx, evt)
	})

	// Blocks until ctx cancelled or fatal error
	err = client.SyncWithContext(ctx)

	// Cleanup
	if ctx.Err() == nil {
		statusCtx, cancelStatus := context.WithTimeout(context.Background(), 5*time.Second)
		if statusErr := p.publishAgentRoomStatus(statusCtx, false); statusErr != nil {
			slog.Warn("matrix: publish agent offline status failed", "error", core.RedactToken(statusErr.Error(), p.accessToken))
		}
		cancelStatus()
	}
	p.closeCryptoHelper()
	p.clearClient(gen, client)
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("matrix: sync ended: %w", err)
}

func (p *Platform) handleMessage(ctx context.Context, evt *event.Event) {
	if !p.roomAllowed(evt.RoomID) {
		return
	}

	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok || content == nil {
		return
	}

	// Skip own messages
	selfID := p.getSelfUserID()
	if evt.Sender == selfID {
		return
	}

	// Dedup
	if p.dedup.IsDuplicate(evt.ID.String()) {
		return
	}

	// Old message check
	msgTime := time.UnixMilli(evt.Timestamp)
	if core.IsOldMessage(msgTime) {
		slog.Debug("matrix: ignoring old message", "event_id", evt.ID, "time", msgTime)
		return
	}

	// Allow-from check
	senderStr := evt.Sender.String()
	if !core.AllowList(p.allowFrom, senderStr) {
		slog.Debug("matrix: message from unauthorized user", "user", senderStr)
		return
	}

	roomID := evt.RoomID
	isDM := p.isDMRoom(ctx, roomID)

	// Group mention check
	if !isDM && !p.groupReplyAll {
		if !p.isDirectedAtBot(content, selfID) {
			return
		}
	}

	userName := displayName(evt.Sender)
	sessionKey := p.buildSessionKey(roomID, evt.Sender)
	channelKey := roomID.String()

	rctx := replyContext{roomID: roomID, messageID: evt.ID}

	// Handle different message types
	msgType := content.MsgType
	switch msgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		text := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: text, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
		})
	case event.MsgImage:
		img, err := p.downloadMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download image failed", "error", err)
			return
		}
		caption := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: caption, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Images: []core.ImageAttachment{*img},
		})
	case event.MsgFile:
		file, err := p.downloadFileMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download file failed", "error", err)
			return
		}
		caption := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: caption, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Files: []core.FileAttachment{*file},
		})
	case event.MsgAudio:
		audio, err := p.downloadAudioMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download audio failed", "error", err)
			return
		}
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			MessageID:  evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Audio: audio,
		})
	default:
		slog.Debug("matrix: ignoring unsupported message type", "type", msgType)
	}
}

func (p *Platform) handleMemberState(ctx context.Context, evt *event.Event) {
	if !p.autoJoin {
		return
	}
	if !p.roomAllowed(evt.RoomID) {
		return
	}
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		return
	}
	selfID := p.getSelfUserID()
	if content.Membership == event.MembershipInvite && evt.StateKey != nil && id.UserID(*evt.StateKey) == selfID {
		client := p.getClient()
		if client == nil {
			return
		}
		_, err := client.JoinRoomByID(ctx, evt.RoomID)
		if err != nil {
			slog.Error("matrix: auto-join failed", "room", evt.RoomID, "error", err)
		} else {
			slog.Info("matrix: auto-joined room", "room", evt.RoomID)
		}
	}
}

func (p *Platform) dispatch(msg *core.Message) {
	handler := p.getHandler()
	if handler == nil {
		return
	}
	handler(p, msg)
}

// sendRoomEvent sends an event to a room, encrypting it if E2EE is available and the room is encrypted.
func (p *Platform) sendRoomEvent(ctx context.Context, roomID id.RoomID, evtType event.Type, content any) error {
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	// Try E2EE path first (only available when built with goolm tag)
	if handled, err := p.tryEncryptAndSend(ctx, client, roomID, evtType, content); handled {
		return err
	}

	_, err := client.SendMessageEvent(ctx, roomID, evtType, content)
	if err != nil {
		return fmt.Errorf("matrix: send: %w", err)
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content
	if content != "" {
		parsed.RelatesTo = &event.RelatesTo{}
		parsed.RelatesTo.SetReplyTo(rc.messageID)
	}

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, p.decorateOutgoingAgentMessage(rc, &parsed, content))
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, p.decorateOutgoingAgentMessage(rc, &parsed, content))
}

func (p *Platform) Stop() error {
	statusCtx, cancelStatus := context.WithTimeout(context.Background(), 5*time.Second)
	if err := p.publishAgentRoomStatus(statusCtx, false); err != nil {
		slog.Warn("matrix: publish agent offline status failed", "error", core.RedactToken(err.Error(), p.accessToken))
	}
	cancelStatus()

	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	cancel := p.cancel
	p.cancel = nil
	p.client = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	p.closeDirexioWSConn()
	return nil
}

func (p *Platform) publishAgentRoomStatus(ctx context.Context, online bool) error {
	if p.direxioWSConfigured() {
		return nil
	}
	p.mu.RLock()
	client := p.client
	roomID := p.allowedRoomID
	userID := p.selfUserID
	if userID == "" {
		userID = id.UserID(p.userID)
	}
	p.mu.RUnlock()

	if client == nil || roomID == "" || userID == "" {
		return nil
	}

	content := map[string]bool{"online": online}
	_, err := client.SendStateEvent(ctx, roomID, agentRoomStatusMatrixEventType, userID.String(), content)
	if err != nil {
		return fmt.Errorf("send %s state event: %w", agentRoomStatusEventType, err)
	}
	return nil
}

func deriveP2PBaseURL(homeserver string) string {
	u, err := url.Parse(strings.TrimSpace(homeserver))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/_p2p"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (p *Platform) direxioWSConfigured() bool {
	return strings.TrimSpace(p.p2pBaseURL) != "" && strings.TrimSpace(p.p2pAgentToken) != ""
}

func (p *Platform) runDirexioAgentWSLoop(ctx context.Context) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		startedAt := time.Now()
		err := p.runDirexioAgentWSOnce(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		wait := backoff
		if time.Since(startedAt) >= stableWindow {
			wait = initialBackoff
			backoff = initialBackoff
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		if err != nil {
			slog.Warn("matrix: direxio agent websocket disconnected, retrying", "error", core.RedactToken(err.Error(), p.p2pAgentToken), "backoff", wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Platform) runDirexioAgentWSOnce(ctx context.Context) error {
	ticket, err := p.createDirexioWSTicket(ctx)
	if err != nil {
		return err
	}
	wsURL, err := p.direxioWSURL(ticket)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	if p.proxyURL != "" {
		if proxy, err := url.Parse(p.proxyURL); err == nil {
			dialer.Proxy = http.ProxyURL(proxy)
		}
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("matrix: connect direxio websocket: %w", err)
	}
	p.setDirexioWSConn(conn)
	defer func() {
		p.clearDirexioWSConn(conn)
		_ = conn.Close()
	}()

	closeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closeDone:
		}
	}()
	defer close(closeDone)

	if err := p.sendDirexioWSFrame(ctx, map[string]any{"type": "client.hello"}); err != nil {
		return err
	}
	pingCtx, cancelPing := context.WithCancel(ctx)
	defer cancelPing()
	go p.runDirexioWSPing(pingCtx)

	for {
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("matrix: read direxio websocket: %w", err)
		}
		if err := p.handleDirexioWSFrame(ctx, frame); err != nil {
			return err
		}
	}
}

func (p *Platform) createDirexioWSTicket(ctx context.Context) (string, error) {
	commandURL, err := p.direxioCommandURL()
	if err != nil {
		return "", err
	}
	body := strings.NewReader(`{"action":"` + realtimeWSTicketAction + `","params":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, commandURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.p2pAgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("matrix: create direxio websocket ticket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("matrix: create direxio websocket ticket: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("matrix: decode direxio websocket ticket: %w", err)
	}
	if strings.TrimSpace(decoded.Ticket) == "" {
		return "", fmt.Errorf("matrix: direxio websocket ticket response missing ticket")
	}
	return decoded.Ticket, nil
}

func (p *Platform) direxioCommandURL() (string, error) {
	u, err := url.Parse(strings.TrimRight(p.p2pBaseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("matrix: invalid p2p_base_url %q: %w", p.p2pBaseURL, err)
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/command") {
		u.Path = strings.TrimRight(u.Path, "/") + "/command"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (p *Platform) direxioWSURL(ticket string) (string, error) {
	u, err := url.Parse(strings.TrimRight(p.p2pBaseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("matrix: invalid p2p_base_url %q: %w", p.p2pBaseURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("matrix: unsupported p2p_base_url scheme %q", u.Scheme)
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/ws") {
		u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	}
	q := u.Query()
	q.Set("ticket", ticket)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func (p *Platform) runDirexioWSPing(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.sendDirexioWSFrame(ctx, map[string]any{"type": "client.ping"})
		}
	}
}

func (p *Platform) handleDirexioWSFrame(ctx context.Context, frame map[string]any) error {
	switch strings.TrimSpace(stringFromAny(frame["type"])) {
	case "server.ready":
		slog.Info("matrix: direxio agent websocket connected", "role", stringFromAny(frame["role"]))
		return nil
	case "server.event":
		return p.handleDirexioAgentRoomEvent(ctx, frame)
	case "server.cursor_reset":
		return nil
	case "server.error":
		msg := strings.TrimSpace(stringFromAny(frame["error"]))
		if msg == "" {
			msg = "server.error"
		}
		return fmt.Errorf("matrix: direxio websocket error: %s", msg)
	default:
		return nil
	}
}

func (p *Platform) handleDirexioAgentRoomEvent(ctx context.Context, frame map[string]any) error {
	rawEvent, ok := mapFromAny(frame["event"])
	if !ok {
		return nil
	}
	if strings.TrimSpace(stringFromAny(rawEvent["type"])) != agentRoomMessageEventType {
		return nil
	}
	payload, _ := mapFromAny(rawEvent["payload"])
	roomIDStr := firstStringFromAny(payload["room_id"], rawEvent["room_id"])
	roomID := id.RoomID(roomIDStr)
	if !p.roomAllowed(roomID) {
		return nil
	}
	eventIDStr := firstStringFromAny(payload["event_id"], rawEvent["event_id"])
	if eventIDStr == "" {
		eventIDStr = fmt.Sprintf("ws-agent-room-%d", int64FromAny(rawEvent["seq"]))
	}
	senderStr := strings.TrimSpace(stringFromAny(payload["sender_mxid"]))
	if senderStr == "" {
		return nil
	}
	selfID := p.getSelfUserID()
	if selfID == "" {
		selfID = id.UserID(p.userID)
	}
	if id.UserID(senderStr) == selfID {
		return nil
	}
	if !core.AllowList(p.allowFrom, senderStr) {
		slog.Debug("matrix: direxio websocket message from unauthorized user", "user", senderStr)
		return nil
	}
	if originTS := int64FromAny(payload["origin_server_ts"]); originTS > 0 && core.IsOldMessage(time.UnixMilli(originTS)) {
		slog.Debug("matrix: ignoring old direxio websocket message", "event_id", eventIDStr, "time", time.UnixMilli(originTS))
		return nil
	}
	msgType := strings.TrimSpace(firstStringFromAny(payload["msgtype"], "m.text"))
	switch event.MessageType(msgType) {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	default:
		return nil
	}
	if p.dedup.IsDuplicate(eventIDStr) {
		return nil
	}
	rawBody := stringFromAny(payload["body"])
	body := stripBotMention(rawBody, selfID)
	isDM := p.isDMRoom(ctx, roomID)
	if !isDM && !p.groupReplyAll {
		if !strings.Contains(rawBody, selfID.String()) {
			return nil
		}
	}
	p.dispatch(&core.Message{
		SessionKey: p.buildSessionKey(roomID, id.UserID(senderStr)),
		Platform:   "matrix",
		UserID:     senderStr,
		UserName:   displayName(id.UserID(senderStr)),
		Content:    body,
		MessageID:  eventIDStr,
		ChannelKey: roomIDStr,
		ReplyCtx: replyContext{
			roomID:    roomID,
			messageID: id.EventID(eventIDStr),
		},
	})
	return nil
}

func (p *Platform) setDirexioWSConn(conn *websocket.Conn) {
	p.p2pMu.Lock()
	old := p.p2pConn
	p.p2pConn = conn
	p.p2pMu.Unlock()
	if old != nil && old != conn {
		_ = old.Close()
	}
}

func (p *Platform) clearDirexioWSConn(conn *websocket.Conn) {
	p.p2pMu.Lock()
	if p.p2pConn == conn {
		p.p2pConn = nil
	}
	p.p2pMu.Unlock()
}

func (p *Platform) closeDirexioWSConn() {
	p.p2pMu.Lock()
	conn := p.p2pConn
	p.p2pConn = nil
	p.p2pMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (p *Platform) sendDirexioAgentStream(ctx context.Context, roomID id.RoomID, streamID, body, finalBody string, done bool) error {
	frame := map[string]any{
		"type":      "client.agent_stream",
		"room_id":   roomID.String(),
		"stream_id": streamID,
		"seq":       p.nextDirexioStreamSeq(),
		"replace":   true,
	}
	if body != "" {
		frame["body"] = body
	}
	if finalBody != "" {
		frame["final_body"] = finalBody
	}
	if done {
		frame["done"] = true
	}
	return p.sendDirexioWSFrame(ctx, frame)
}

func (p *Platform) sendDirexioWSFrame(ctx context.Context, frame map[string]any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.p2pMu.Lock()
	conn := p.p2pConn
	p.p2pMu.Unlock()
	if conn == nil {
		return fmt.Errorf("matrix: direxio websocket not connected")
	}
	p.p2pWriteMu.Lock()
	defer p.p2pWriteMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := conn.WriteJSON(frame)
	_ = conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("matrix: write direxio websocket: %w", err)
	}
	return nil
}

func (p *Platform) nextDirexioStreamSeq() int64 {
	p.p2pMu.Lock()
	defer p.p2pMu.Unlock()
	p.p2pStreamSeq++
	return p.p2pStreamSeq
}

func (p *Platform) decorateOutgoingAgentMessage(rc replyContext, content *event.MessageEventContent, finalBody string) any {
	if !p.direxioWSConfigured() || p.allowedRoomID == "" || rc.roomID != p.allowedRoomID {
		return content
	}
	streamID := strings.TrimSpace(rc.messageID.String())
	if streamID == "" {
		streamID = fmt.Sprintf("matrix-final-%d", time.Now().UnixNano())
	}
	return &agentGatewayMessageContent{
		MessageEventContent: content,
		AgentGateway:        true,
		GatewaySource:       agentGatewaySource,
		AgentStream: map[string]any{
			"stream_id":  streamID,
			"seq":        p.nextDirexioStreamSeq(),
			"final_body": finalBody,
			"done":       true,
			"replace":    true,
		},
	}
}

func mapFromAny(raw any) (map[string]any, bool) {
	if raw == nil {
		return nil, false
	}
	if m, ok := raw.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func firstStringFromAny(values ...any) string {
	for _, value := range values {
		if s := strings.TrimSpace(stringFromAny(value)); s != "" {
			return s
		}
	}
	return ""
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		var n int64
		_, _ = fmt.Sscan(strings.TrimSpace(v), &n)
		return n
	default:
		return 0
	}
}

// --- Optional interfaces ---

func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	mime := img.MimeType
	if mime == "" {
		mime = "image/png"
	}
	name := img.FileName
	if name == "" {
		name = "image"
	}

	uri, err := client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: img.Data,
		ContentType:  mime,
		FileName:     name,
	})
	if err != nil {
		return fmt.Errorf("matrix: upload image: %w", err)
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     len(img.Data),
		},
	}
	if !uri.ContentURI.IsEmpty() {
		content.URL = uri.ContentURI.CUString()
	} else {
		content.File = &event.EncryptedFileInfo{
			URL: uri.ContentURI.CUString(),
		}
	}

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
}

func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	mime := file.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	name := file.FileName
	if name == "" {
		name = "attachment"
	}

	uri, err := client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: file.Data,
		ContentType:  mime,
		FileName:     name,
	})
	if err != nil {
		return fmt.Errorf("matrix: upload file: %w", err)
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgFile,
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     len(file.Data),
		},
	}
	if !uri.ContentURI.IsEmpty() {
		content.URL = uri.ContentURI.CUString()
	} else {
		content.File = &event.EncryptedFileInfo{
			URL: uri.ContentURI.CUString(),
		}
	}

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
}

func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}

	client := p.getClient()
	if client == nil {
		return func() {}
	}

	// Set typing with 30s timeout, refresh every 25s
	_, _ = client.UserTyping(ctx, rc.roomID, true, 30*time.Second)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				c := p.getClient()
				if c != nil {
					_, _ = c.UserTyping(context.Background(), rc.roomID, false, 0)
				}
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				c := p.getClient()
				if c == nil {
					return
				}
				_, _ = c.UserTyping(ctx, rc.roomID, true, 30*time.Second)
			}
		}
	}()

	return func() { close(done) }
}

func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	if handle, ok := previewHandle.(*agentStreamPreviewHandle); ok {
		handle.mu.Lock()
		handle.lastBody = content
		handle.mu.Unlock()
		return p.sendDirexioAgentStream(ctx, handle.roomID, handle.streamID, content, "", false)
	}
	rc, ok := previewHandle.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid preview handle type %T", previewHandle)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content

	// Copy the new content for m.replace relation
	newContent := parsed
	newContent.Mentions = nil

	parsed.NewContent = &newContent
	parsed.RelatesTo = &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: rc.messageID,
	}
	parsed.Body = "* " + content

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
}

func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}
	if !p.direxioWSConfigured() {
		if err := p.Send(ctx, rctx, content); err != nil {
			return nil, err
		}
		return rc, nil
	}
	streamID := strings.TrimSpace(rc.messageID.String())
	if streamID == "" {
		streamID = fmt.Sprintf("matrix-preview-%d", time.Now().UnixNano())
	}
	handle := &agentStreamPreviewHandle{
		roomID:   rc.roomID,
		streamID: streamID,
		lastBody: content,
	}
	if err := p.sendDirexioAgentStream(ctx, rc.roomID, streamID, content, "", false); err != nil {
		return nil, err
	}
	return handle, nil
}

func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	handle, ok := previewHandle.(*agentStreamPreviewHandle)
	if !ok {
		return nil
	}
	handle.mu.Lock()
	finalBody := handle.lastBody
	handle.mu.Unlock()
	return p.sendDirexioAgentStream(ctx, handle.roomID, handle.streamID, "", finalBody, true)
}

func (p *Platform) KeepPreviewOnFinish() bool {
	return !p.direxioWSConfigured()
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// Formats:
	//   matrix:{roomID}:{userID}   - per-user session
	//   matrix:{roomID}            - shared session
	// Room IDs contain a colon (!localpart:server), so we can't simply split on colons.
	if !strings.HasPrefix(sessionKey, "matrix:") {
		return nil, fmt.Errorf("matrix: invalid session key %q", sessionKey)
	}
	rest := sessionKey[len("matrix:"):]
	if rest == "" {
		return nil, fmt.Errorf("matrix: invalid session key %q", sessionKey)
	}

	// Find boundary between room ID and optional user ID.
	// User IDs start with @, so ":@" only appears at the roomID:userID boundary.
	var roomIDStr string
	if idx := strings.Index(rest, ":@"); idx >= 0 {
		roomIDStr = rest[:idx]
	} else {
		roomIDStr = rest
	}

	if !strings.HasPrefix(roomIDStr, "!") {
		return nil, fmt.Errorf("matrix: invalid room ID in %q", sessionKey)
	}
	if !p.roomAllowed(id.RoomID(roomIDStr)) {
		return nil, fmt.Errorf("matrix: room %q is not allowed", roomIDStr)
	}
	return replyContext{roomID: id.RoomID(roomIDStr)}, nil
}

// --- Internal helpers ---

func (p *Platform) roomAllowed(roomID id.RoomID) bool {
	return p.allowedRoomID == "" || roomID == p.allowedRoomID
}

func (p *Platform) buildSessionKey(roomID id.RoomID, sender id.UserID) string {
	if p.shareSessionInChannel {
		return fmt.Sprintf("matrix:%s", roomID)
	}
	return fmt.Sprintf("matrix:%s:%s", roomID, sender)
}

func (p *Platform) isDMRoom(ctx context.Context, roomID id.RoomID) bool {
	client := p.getClient()
	if client == nil {
		return false
	}
	members, err := client.Members(ctx, roomID)
	if err != nil {
		slog.Debug("matrix: failed to get room members, assuming group", "error", err)
		return false
	}
	return len(members.Chunk) <= 2
}

func (p *Platform) isDirectedAtBot(content *event.MessageEventContent, selfID id.UserID) bool {
	// Check formatted body for matrix.to link
	if content.FormattedBody != "" {
		mention := fmt.Sprintf("https://matrix.to/#/%s", selfID)
		if strings.Contains(content.FormattedBody, mention) {
			return true
		}
	}
	// Check plain body for @user:server mention
	if strings.Contains(content.Body, selfID.String()) {
		return true
	}
	return false
}

func (p *Platform) downloadMediaContent(ctx context.Context, contentURL id.ContentURIString) ([]byte, error) {
	client := p.getClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	parsed, err := contentURL.Parse()
	if err != nil {
		return nil, fmt.Errorf("parse content URI: %w", err)
	}
	resp, err := client.Download(ctx, parsed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

func (p *Platform) downloadMedia(ctx context.Context, content *event.MessageEventContent) (*core.ImageAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = "image/png"
	}
	name := content.Body
	return &core.ImageAttachment{
		MimeType: mime,
		Data:     data,
		FileName: name,
	}, nil
}

func (p *Platform) downloadFileMedia(ctx context.Context, content *event.MessageEventContent) (*core.FileAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return &core.FileAttachment{
		MimeType: mime,
		Data:     data,
		FileName: content.Body,
	}, nil
}

func (p *Platform) downloadAudioMedia(ctx context.Context, content *event.MessageEventContent) (*core.AudioAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	audiFmt := ""
	duration := 0
	if content.Info != nil {
		mime = content.Info.MimeType
		duration = content.Info.Duration / 1000
	}
	if mime == "" {
		mime = "audio/ogg"
	}
	if parts := strings.SplitN(mime, "/", 2); len(parts) == 2 {
		audiFmt = parts[1]
	}
	if audiFmt == "" {
		audiFmt = "ogg"
	}
	return &core.AudioAttachment{
		MimeType: mime,
		Data:     data,
		Format:   audiFmt,
		Duration: duration,
	}, nil
}

func stripBotMention(text string, selfID id.UserID) string {
	if selfID == "" {
		return text
	}
	// Strip matrix.to links first (longer pattern), then plain user ID
	text = strings.ReplaceAll(text, fmt.Sprintf("https://matrix.to/#/%s", selfID), "")
	text = strings.ReplaceAll(text, selfID.String(), "")
	return strings.TrimSpace(text)
}

func displayName(userID id.UserID) string {
	// Use the localpart as a fallback display name
	localpart, _, _ := strings.Cut(userID.String(), ":")
	return strings.TrimPrefix(localpart, "@")
}

// --- Concurrency-safe accessors ---

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

func (p *Platform) getClient() *mautrix.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.client
}

func (p *Platform) getSelfUserID() id.UserID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.selfUserID
}

func (p *Platform) getHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) publishClient(client *mautrix.Client, selfUserID id.UserID) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return 0, false
	}
	p.generation++
	p.client = client
	p.selfUserID = selfUserID
	return p.generation, true
}

func (p *Platform) emitReady(gen uint64) {
	p.mu.RLock()
	if p.stopping || p.generation != gen || p.client == nil {
		p.mu.RUnlock()
		return
	}
	handler := p.lifecycleHandler
	p.mu.RUnlock()

	p.mu.Lock()
	p.everConnected = true
	p.unavailableNotified = false
	p.mu.Unlock()

	if handler != nil {
		handler.OnPlatformReady(p)
	}
}

func (p *Platform) clearClient(gen uint64, client *mautrix.Client) {
	notify := false
	p.mu.Lock()
	if p.client == client && p.generation == gen {
		p.client = nil
		notify = !p.stopping
	}
	p.mu.Unlock()

	if notify {
		p.notifyUnavailable(fmt.Errorf("matrix: connection lost"))
	}
}

func (p *Platform) notifyUnavailable(err error) {
	var handler core.PlatformLifecycleHandler

	p.mu.Lock()
	if p.stopping || err == nil || p.unavailableNotified {
		p.mu.Unlock()
		return
	}
	p.unavailableNotified = true
	handler = p.lifecycleHandler
	p.mu.Unlock()

	if handler != nil {
		handler.OnPlatformUnavailable(p, err)
	}
}

// Interface compliance checks
var (
	_ core.Platform                  = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform  = (*Platform)(nil)
	_ core.ReplyContextReconstructor = (*Platform)(nil)
	_ core.ImageSender               = (*Platform)(nil)
	_ core.FileSender                = (*Platform)(nil)
	_ core.MessageUpdater            = (*Platform)(nil)
	_ core.PreviewStarter            = (*Platform)(nil)
	_ core.PreviewCleaner            = (*Platform)(nil)
	_ core.PreviewFinishPreference   = (*Platform)(nil)
	_ core.TypingIndicator           = (*Platform)(nil)
)
