package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
)

const (
	maxMCPMessageBytes  = 16 << 20 // 16 MiB per single JSON-RPC line
	maxMCPResponseBytes = 32 << 20 // 32 MiB aggregate per HTTP/SSE response
	maxMCPConcurrency   = 16
)

// BridgeOptions configures one MCP tunnel bridge session.
type BridgeOptions struct {
	UserID       string
	AgentID      string
	SessionID    string
	ApprovalMode string
	// Authorize, if set, is called on every incoming JSON-RPC request and returns
	// the agent's current approval mode or an error if access was revoked.
	Authorize func(ctx context.Context) (approvalMode string, err error)
	// ResolveUser, if set, is called on every incoming JSON-RPC request to
	// resolve which user's Executor instance should handle the call (per-caller
	// identity routing). If it returns an error or empty user ID, the call is
	// refused without falling back to UserID.
	ResolveUser func(ctx context.Context) (userID string, err error)
}

var errSessionNotFound = errors.New("executor MCP session not found")

// BridgeMCP relays newline-delimited JSON-RPC 2.0 messages over stream (from a
// sandbox's `sandboxd executor-mcp` stdio server) to the acting caller's managed
// Executor Streamable HTTP /mcp endpoint. Credentials never leave the host.
func (m *Manager) BridgeMCP(ctx context.Context, opts BridgeOptions, stream io.ReadWriteCloser) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	unregister := m.registerStream(opts.UserID, opts.AgentID, cancel)
	defer unregister()
	defer stream.Close()

	// On forced cancellation (revocation, parent shutdown), set an immediate read
	// deadline (yamux.Stream.Close only closes the write side) and close the stream
	// so sc.Scan() unblocks immediately.
	stopWatch := context.AfterFunc(streamCtx, func() {
		if dl, ok := stream.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = dl.SetReadDeadline(time.Now())
		}
		_ = stream.Close()
	})
	defer stopWatch()

	mcpClient := &http.Client{Timeout: 10 * time.Minute}

	var (
		writeMu        sync.Mutex
		sessionMu      sync.Mutex
		sessionsByUser = map[string]string{}
		initReq        []byte
	)

	writeFrame := func(raw []byte, instURL string) error {
		rewritten := rewriteLoopbackURLs(raw, instURL, m.cfg.DashboardOrigin)
		var buf bytes.Buffer
		if err := json.Compact(&buf, rewritten); err != nil {
			return err
		}
		buf.WriteByte('\n')
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err := stream.Write(buf.Bytes())
		return err
	}

	writeRPCError := func(id json.RawMessage, code int, msg string) {
		if len(id) == 0 || bytes.Equal(id, []byte("null")) {
			return
		}
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": code, "message": msg},
		})
		if err != nil {
			return
		}
		b = append(b, '\n')
		writeMu.Lock()
		_, _ = stream.Write(b)
		writeMu.Unlock()
	}

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)

	sem := make(chan struct{}, maxMCPConcurrency)
	var wg sync.WaitGroup
	// On graceful stdin EOF, wait for in-flight workers to finish writing their
	// final responses before cancelling streamCtx. Forced cancellation already
	// cancelled streamCtx, which interrupts workers immediately.
	defer func() {
		wg.Wait()
		cancel()
	}()

	handleLine := func(line []byte) {
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return
		}

		mode := NormalizeApprovalMode(opts.ApprovalMode)
		if opts.Authorize != nil {
			curMode, err := opts.Authorize(streamCtx)
			if err != nil {
				writeRPCError(env.ID, -32001, "executor access revoked: "+err.Error())
				cancel()
				return
			}
			mode = NormalizeApprovalMode(curMode)
		}

		targetUser := opts.UserID
		if opts.ResolveUser != nil {
			resolved, err := opts.ResolveUser(streamCtx)
			if err != nil {
				writeRPCError(env.ID, -32001, "executor caller unauthorized: "+err.Error())
				return
			}
			if strings.TrimSpace(resolved) == "" {
				writeRPCError(env.ID, -32001, "executor caller could not be resolved")
				return
			}
			targetUser = resolved
		}

		if env.Method == "initialize" {
			sessionMu.Lock()
			initReq = line
			sessionMu.Unlock()
		}
		if env.Method == "tools/call" && m.cfg.Audit != nil {
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(env.Params, &p)
			details := map[string]any{"mode": mode}
			if opts.SessionID != "" {
				details["sessionId"] = opts.SessionID
			}
			m.cfg.Audit.Record(streamCtx, audit.Entry{
				AgentID: opts.AgentID,
				UserID:  targetUser,
				Action:  audit.ExecutorCall,
				Target:  p.Name,
				Details: details,
			})
		}

		inst, release, err := m.acquireInstance(streamCtx, targetUser)
		if err != nil {
			writeRPCError(env.ID, -32603, "executor unavailable: "+err.Error())
			return
		}
		defer release()

		sessionMu.Lock()
		curSID := sessionsByUser[targetUser]
		hasInit := len(initReq) > 0
		sessionMu.Unlock()

		// When a different collaborator's turn triggers an MCP call over an
		// already-initialized harness bridge, initialize a fresh MCP session on
		// that collaborator's own Executor instance before forwarding the call.
		if curSID == "" && hasInit && env.Method != "initialize" {
			if refreshedSID, refreshErr := m.reinitMCPSessionForUser(streamCtx, mcpClient, inst, mode, &sessionMu, sessionsByUser, targetUser, &initReq, ""); refreshErr == nil && refreshedSID != "" {
				curSID = refreshedSID
			}
		}

		emitted := false
		emit := func(msg []byte) error {
			emitted = true
			return writeFrame(msg, inst.URL)
		}

		newSID, status, err := m.postMCPStream(streamCtx, mcpClient, inst, mode, curSID, line, emit)
		if (errors.Is(err, errSessionNotFound) || (status == http.StatusNotFound && hasInit)) && hasInit && env.Method != "initialize" {
			refreshedSID, refreshErr := m.reinitMCPSessionForUser(streamCtx, mcpClient, inst, mode, &sessionMu, sessionsByUser, targetUser, &initReq, curSID)
			if refreshErr == nil && refreshedSID != "" {
				emitted = false
				newSID, status, err = m.postMCPStream(streamCtx, mcpClient, inst, mode, refreshedSID, line, emit)
			}
		}

		if err != nil {
			if !emitted && streamCtx.Err() == nil {
				writeRPCError(env.ID, -32603, "executor bridge: "+err.Error())
			}
			return
		}
		if newSID != "" {
			sessionMu.Lock()
			sessionsByUser[targetUser] = newSID
			sessionMu.Unlock()
		}
		if (status < 200 || status >= 300) && !emitted {
			writeRPCError(env.ID, -32603, fmt.Sprintf("executor MCP returned HTTP %d", status))
		}
	}

	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		line := append([]byte(nil), raw...)

		// Serialize "initialize" synchronously so subsequent concurrent calls see sessionID.
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.Method == "initialize" {
			handleLine(line)
			if streamCtx.Err() != nil {
				return
			}
			continue
		}

		select {
		case sem <- struct{}{}:
		case <-streamCtx.Done():
			return
		}
		wg.Add(1)
		go func(payload []byte) {
			defer wg.Done()
			defer func() { <-sem }()
			handleLine(payload)
		}(line)
	}
}

func (m *Manager) reinitMCPSessionForUser(
	ctx context.Context,
	client *http.Client,
	inst *Instance,
	mode string,
	sessionMu *sync.Mutex,
	sessionsByUser map[string]string,
	userID string,
	initReq *[]byte,
	failedSID string,
) (string, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	// Another concurrent worker may already have refreshed this user's session.
	if cur := sessionsByUser[userID]; cur != "" && cur != failedSID {
		return cur, nil
	}
	if len(*initReq) == 0 {
		return "", errors.New("no initialize request recorded")
	}
	discard := func([]byte) error { return nil }
	refreshedSID, status, err := m.postMCPStream(ctx, client, inst, mode, "", *initReq, discard)
	if err != nil || status < 200 || status >= 300 || refreshedSID == "" {
		if err == nil {
			err = fmt.Errorf("re-initialize returned HTTP %d", status)
		}
		return "", err
	}
	sessionsByUser[userID] = refreshedSID
	_, _, _ = m.postMCPStream(ctx, client, inst, mode, refreshedSID, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), discard)
	return refreshedSID, nil
}

func (m *Manager) postMCPStream(
	ctx context.Context,
	client *http.Client,
	inst *Instance,
	mode string,
	sessionID string,
	payload []byte,
	emit func([]byte) error,
) (returnedSessionID string, status int, err error) {
	inst.touch()
	endpoint := inst.URL + "/mcp?elicitation_mode=" + mode
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+inst.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	returnedSessionID = resp.Header.Get("Mcp-Session-Id")
	status = resp.StatusCode
	if status == http.StatusNotFound && sessionID != "" {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return returnedSessionID, status, errSessionNotFound
	}
	if status == http.StatusAccepted || status == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return returnedSessionID, status, nil
	}

	limited := &boundedReader{r: resp.Body, limit: maxMCPResponseBytes}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/event-stream") {
		if err := streamSSEJSONMessages(limited, emit); err != nil {
			return returnedSessionID, status, err
		}
		return returnedSessionID, status, nil
	}
	body, err := io.ReadAll(limited)
	if err != nil {
		return returnedSessionID, status, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return returnedSessionID, status, nil
	}
	if json.Valid(body) {
		return returnedSessionID, status, emit(body)
	}
	if status >= 400 {
		return returnedSessionID, status, errors.New(string(body))
	}
	return returnedSessionID, status, fmt.Errorf("unexpected non-JSON MCP response (%s)", ct)
}

type boundedReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.read > b.limit {
		return 0, errors.New("executor MCP response exceeded maximum size")
	}
	maxRead := b.limit - b.read + 1
	if int64(len(p)) > maxRead {
		p = p[:maxRead]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errors.New("executor MCP response exceeded maximum size")
	}
	return n, err
}

func streamSSEJSONMessages(r io.Reader, emit func([]byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	var dataBuf bytes.Buffer
	flush := func() error {
		if dataBuf.Len() == 0 {
			return nil
		}
		payload := bytes.TrimSpace(dataBuf.Bytes())
		dataBuf.Reset()
		if len(payload) == 0 || !json.Valid(payload) {
			return nil
		}
		return emit(payload)
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimPrefix(rest, " "))
			if dataBuf.Len() > maxMCPMessageBytes {
				return errors.New("executor SSE event exceeded maximum size")
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

// rewriteLoopbackURLs replaces local loopback Executor URLs in MCP responses
// with the dedicated Agenthold Executor dashboard origin when configured.
func rewriteLoopbackURLs(msg []byte, loopbackURL, dashboardOrigin string) []byte {
	if dashboardOrigin == "" || loopbackURL == "" || !bytes.Contains(msg, []byte(loopbackURL)) {
		return msg
	}
	return bytes.ReplaceAll(msg, []byte(loopbackURL), []byte(dashboardOrigin))
}
