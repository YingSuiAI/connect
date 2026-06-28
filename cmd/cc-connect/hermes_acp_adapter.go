package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

const hermesACPOutputContract = `DIREXIO ACP OUTPUT CONTRACT:
Your response is sent directly to a Direxio user.
Return only the final user-visible answer.
Do not include reasoning, analysis, hidden thoughts, or restatements of the user's request.
If the user asks for an exact short reply, output exactly that reply.
最终答案只能包含给用户看的内容，不要输出“用户让我...”“The user...”等推理说明。`

var thinkTagRe = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>`)

type hermesACPAdapter struct {
	mu             sync.Mutex
	promptRequests map[string]hermesPromptRequest
	buffers        map[string]*strings.Builder
	lastSessionID  string
}

type hermesPromptRequest struct {
	sessionID string
}

func newHermesACPAdapter() *hermesACPAdapter {
	return &hermesACPAdapter{
		promptRequests: make(map[string]hermesPromptRequest),
		buffers:        make(map[string]*strings.Builder),
	}
}

func runHermesACPAdapter(args []string) error {
	childArgs := args
	if len(childArgs) > 0 && childArgs[0] == "--" {
		childArgs = childArgs[1:]
	}
	if len(childArgs) == 0 {
		childArgs = []string{"hermes", "acp"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := exec.CommandContext(ctx, childArgs[0], childArgs[1:]...)
	cmd.Stderr = os.Stderr

	childIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("hermes-acp-adapter: child stdin: %w", err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("hermes-acp-adapter: child stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hermes-acp-adapter: start %s: %w", childArgs[0], err)
	}

	adapter := newHermesACPAdapter()
	errCh := make(chan error, 2)
	go func() {
		defer childIn.Close()
		errCh <- adapter.copyParentToChild(os.Stdin, childIn)
	}()
	go func() {
		errCh <- adapter.copyChildToParent(childOut, os.Stdout)
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	for {
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, io.EOF) {
				stop()
				_ = cmd.Process.Kill()
				return err
			}
		case waitErr := <-waitCh:
			stop()
			if waitErr != nil {
				return fmt.Errorf("hermes-acp-adapter: child exited: %w", waitErr)
			}
			return nil
		}
	}
}

func (a *hermesACPAdapter) copyParentToChild(in io.Reader, out io.Writer) error {
	return copyACPJSONLines(in, out, func(line []byte) ([][]byte, error) {
		rewritten, ok, err := a.rewriteParentLine(line)
		if err != nil || !ok {
			return nil, err
		}
		return [][]byte{rewritten}, nil
	})
}

func (a *hermesACPAdapter) copyChildToParent(in io.Reader, out io.Writer) error {
	return copyACPJSONLines(in, out, a.rewriteChildLine)
}

func copyACPJSONLines(in io.Reader, out io.Writer, rewrite func([]byte) ([][]byte, error)) error {
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			lines, rewriteErr := rewrite(line)
			if rewriteErr != nil {
				return rewriteErr
			}
			for _, outLine := range lines {
				if _, writeErr := out.Write(append(outLine, '\n')); writeErr != nil {
					return writeErr
				}
			}
		}
		if err != nil {
			return err
		}
	}
}

func (a *hermesACPAdapter) rewriteParentLine(line []byte) ([]byte, bool, error) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &env); err != nil || env.Method != "session/prompt" || hermesJSONRPCIDAbsent(env.ID) {
		return line, true, nil
	}

	var params map[string]any
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return line, true, nil
	}
	sessionID, _ := params["sessionId"].(string)
	prompt, _ := params["prompt"].([]any)
	params["prompt"] = append(prompt, map[string]any{
		"type": "text",
		"text": hermesACPOutputContract,
	})

	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return nil, false, err
	}
	obj["params"] = params
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false, err
	}

	a.mu.Lock()
	a.promptRequests[hermesJSONRPCIDKey(env.ID)] = hermesPromptRequest{sessionID: sessionID}
	if sessionID != "" {
		a.lastSessionID = sessionID
	}
	a.mu.Unlock()

	return out, true, nil
}

func (a *hermesACPAdapter) rewriteChildLine(line []byte) ([][]byte, error) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return [][]byte{line}, nil
	}

	if env.Method == "session/update" {
		sessionID, kind, text := extractHermesSessionUpdateText(env.Params)
		if sessionID != "" {
			a.mu.Lock()
			a.lastSessionID = sessionID
			a.mu.Unlock()
		}
		if kind == "agent_message_chunk" && text != "" {
			a.bufferText(sessionID, text)
			return nil, nil
		}
		return [][]byte{line}, nil
	}

	if !hermesJSONRPCIDAbsent(env.ID) {
		key := hermesJSONRPCIDKey(env.ID)
		if req, ok := a.takePromptRequest(key); ok {
			sessionID := req.sessionID
			if sessionID == "" {
				sessionID = a.currentSessionID()
			}
			text := a.takeBufferedText(sessionID)
			if text == "" {
				text = extractHermesResultText(env.Result)
			}
			text = sanitizeHermesVisibleText(text)
			if text != "" {
				flushed, err := buildHermesMessageChunk(sessionID, text)
				if err != nil {
					return nil, err
				}
				return [][]byte{flushed, line}, nil
			}
		}
	}

	return [][]byte{line}, nil
}

func (a *hermesACPAdapter) bufferText(sessionID string, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID == "" {
		sessionID = a.lastSessionID
	}
	if sessionID == "" {
		sessionID = "_default"
	}
	buf := a.buffers[sessionID]
	if buf == nil {
		buf = &strings.Builder{}
		a.buffers[sessionID] = buf
	}
	buf.WriteString(text)
}

func (a *hermesACPAdapter) takePromptRequest(id string) (hermesPromptRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	req, ok := a.promptRequests[id]
	delete(a.promptRequests, id)
	return req, ok
}

func (a *hermesACPAdapter) currentSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastSessionID
}

func (a *hermesACPAdapter) takeBufferedText(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID == "" {
		sessionID = a.lastSessionID
	}
	if sessionID == "" {
		sessionID = "_default"
	}
	buf := a.buffers[sessionID]
	if buf == nil {
		return ""
	}
	delete(a.buffers, sessionID)
	return buf.String()
}

func extractHermesSessionUpdateText(params json.RawMessage) (sessionID string, kind string, text string) {
	var wrap struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return "", "", ""
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       any    `json:"content"`
		Text          string `json:"text"`
		Message       string `json:"message"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return wrap.SessionID, "", ""
	}
	text = head.Text
	if text == "" {
		text = head.Message
	}
	if text == "" {
		text = extractContentText(wrap.Update)
	}
	return wrap.SessionID, strings.ToLower(strings.TrimSpace(head.SessionUpdate)), text
}

func extractHermesResultText(result json.RawMessage) string {
	if len(bytes.TrimSpace(result)) == 0 {
		return ""
	}
	var r struct {
		FinalResponse string `json:"final_response"`
		FinalAnswer   string `json:"final_answer"`
		Response      string `json:"response"`
		Text          string `json:"text"`
		Message       string `json:"message"`
		Content       any    `json:"content"`
	}
	if json.Unmarshal(result, &r) != nil {
		return ""
	}
	switch {
	case r.FinalResponse != "":
		return r.FinalResponse
	case r.FinalAnswer != "":
		return r.FinalAnswer
	case r.Response != "":
		return r.Response
	case r.Text != "":
		return r.Text
	case r.Message != "":
		return r.Message
	default:
		return extractContentText(result)
	}
}

func extractContentText(raw json.RawMessage) string {
	var withContent struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &withContent) != nil || len(withContent.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(withContent.Content, &s) == nil {
		return s
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(withContent.Content, &obj) == nil && obj.Text != "" {
		return obj.Text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(withContent.Content, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(block.Text)
		}
		return b.String()
	}
	return ""
}

func buildHermesMessageChunk(sessionID string, text string) ([]byte, error) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": text,
				},
			},
		},
	}
	return json.Marshal(msg)
}

func sanitizeHermesVisibleText(text string) string {
	text = strings.TrimSpace(thinkTagRe.ReplaceAllString(text, ""))
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return ""
	}
	if after := textAfterLastMarker(text, []string{
		"最终答案：", "最终答案:", "最终回答：", "最终回答:", "答案：", "答案:",
		"Final answer:", "Final Answer:", "Response:", "Assistant:",
	}); after != "" {
		return after
	}
	if looksLikeHermesMetaNarration(text) {
		if line := lastNonEmptyLine(text); line != "" && line != text && !looksLikeHermesMetaNarration(line) {
			return line
		}
		if tail := shortTailAfterSentenceBoundary(text); tail != "" && !looksLikeHermesMetaNarration(tail) {
			return tail
		}
	}
	return text
}

func textAfterLastMarker(text string, markers []string) string {
	best := -1
	bestMarker := ""
	for _, marker := range markers {
		if i := strings.LastIndex(text, marker); i > best {
			best = i
			bestMarker = marker
		}
	}
	if best < 0 {
		return ""
	}
	return strings.TrimSpace(text[best+len(bestMarker):])
}

func looksLikeHermesMetaNarration(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	prefixes := []string{
		"the user ",
		"user asked ",
		"user wants ",
		"用户",
		"这个用户",
		"该用户",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func shortTailAfterSentenceBoundary(text string) string {
	trimmed := strings.TrimSpace(text)
	boundaries := []string{"。", ". ", "！", "! ", "？", "? "}
	best := -1
	bestLen := 0
	for _, boundary := range boundaries {
		if i := strings.LastIndex(trimmed, boundary); i >= 0 && i+len(boundary) > best {
			best = i + len(boundary)
			bestLen = len(boundary)
		}
	}
	if best < 0 || best > len(trimmed) {
		return ""
	}
	tail := strings.TrimSpace(trimmed[best:])
	if tail == "" && bestLen > 0 {
		return ""
	}
	if len([]rune(tail)) > 120 {
		return ""
	}
	return tail
}

func hermesJSONRPCIDAbsent(id json.RawMessage) bool {
	return len(bytes.TrimSpace(id)) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}

func hermesJSONRPCIDKey(id json.RawMessage) string {
	id = bytes.TrimSpace(id)
	var n json.Number
	if json.Unmarshal(id, &n) == nil {
		return string(n)
	}
	var s string
	if json.Unmarshal(id, &s) == nil {
		return s
	}
	return string(id)
}
