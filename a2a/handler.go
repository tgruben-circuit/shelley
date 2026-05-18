package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Backend is the integration surface the A2A handler needs from Percy.
// Implemented by *server.A2ABackend in the parent package.
type Backend interface {
	// NewConversation creates a fresh root conversation (no parent) and returns its ID.
	NewConversation(ctx context.Context, cwd string) (string, error)
	// SendMessage delivers a user prompt to the conversation and blocks until the
	// agent finishes or timeout elapses. Returns the assistant's final text.
	SendMessage(ctx context.Context, conversationID, prompt string, timeout time.Duration) (string, error)
	// Cancel cancels an in-flight turn for the given conversation.
	Cancel(ctx context.Context, conversationID string) error
	// SubscribeMessages returns a channel of agent text messages emitted while the
	// agent processes the next turn. The channel closes when the turn finishes.
	// SendMessage and SubscribeMessages may be used together for streaming.
	SubscribeMessages(ctx context.Context, conversationID string) (<-chan string, error)
}

// Handler implements the A2A HTTP surface. Mount it under any prefix; it
// expects to receive requests for /.well-known/agent-card.json and /a2a.
type Handler struct {
	Backend Backend
	Card    AgentCard
	Token   string // shared bearer token; required

	mu    sync.RWMutex
	tasks map[string]*taskRecord // taskID -> record
}

type taskRecord struct {
	Task           *Task
	ConversationID string
	Cancel         context.CancelFunc
}

func New(backend Backend, card AgentCard, token string) *Handler {
	return &Handler{
		Backend: backend,
		Card:    card,
		Token:   token,
		tasks:   make(map[string]*taskRecord),
	}
}

// ServeHTTP dispatches to the agent card or the JSON-RPC endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/agent-card.json":
		h.serveAgentCard(w, r)
	case "/a2a":
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.Token == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+h.Token
}

func (h *Handler) serveAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.Card)
}

func (h *Handler) serveRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, errParseError, "invalid JSON: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, errInvalidRequest, "jsonrpc must be \"2.0\"")
		return
	}
	switch req.Method {
	case "message/send":
		h.handleMessageSend(w, r, req)
	case "message/stream":
		h.handleMessageStream(w, r, req)
	case "tasks/get":
		h.handleTaskGet(w, r, req)
	case "tasks/cancel":
		h.handleTaskCancel(w, r, req)
	default:
		writeRPCError(w, req.ID, errMethodNotFound, "method not supported: "+req.Method)
	}
}

// message/send -------------------------------------------------------------

func (h *Handler) handleMessageSend(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params MessageSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}
	prompt, err := promptFromMessage(params.Message)
	if err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}

	ctx := r.Context()
	convID := params.Message.ContextID
	if convID == "" {
		convID, err = h.Backend.NewConversation(ctx, "")
		if err != nil {
			writeRPCError(w, req.ID, errInternal, "failed to create conversation: "+err.Error())
			return
		}
	}

	taskID := "task-" + uuid.NewString()
	task := &Task{Kind: "task", ID: taskID, ContextID: convID, Status: TaskStatus{State: TaskStateSubmitted, Timestamp: nowRFC3339()}}
	rec := &taskRecord{Task: task, ConversationID: convID}
	h.mu.Lock()
	h.tasks[taskID] = rec
	h.mu.Unlock()

	task.Status.State = TaskStateWorking
	task.Status.Timestamp = nowRFC3339()

	timeout := 5 * time.Minute
	replyText, err := h.Backend.SendMessage(ctx, convID, prompt, timeout)
	if err != nil {
		h.mu.Lock()
		task.Status.State = TaskStateFailed
		task.Status.Timestamp = nowRFC3339()
		h.mu.Unlock()
		writeRPCError(w, req.ID, errInternal, err.Error())
		return
	}

	replyMsg := &Message{
		Kind:      "message",
		MessageID: "msg-" + uuid.NewString(),
		Role:      "agent",
		Parts:     []Part{{Kind: "text", Text: replyText}},
		ContextID: convID,
		TaskID:    taskID,
	}
	h.mu.Lock()
	task.Status.State = TaskStateCompleted
	task.Status.Message = replyMsg
	task.Status.Timestamp = nowRFC3339()
	task.History = []Message{params.Message, *replyMsg}
	h.mu.Unlock()

	writeRPCResult(w, req.ID, task)
}

// message/stream -----------------------------------------------------------

func (h *Handler) handleMessageStream(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params MessageSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}
	prompt, err := promptFromMessage(params.Message)
	if err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	convID := params.Message.ContextID
	if convID == "" {
		convID, err = h.Backend.NewConversation(ctx, "")
		if err != nil {
			writeRPCError(w, req.ID, errInternal, err.Error())
			return
		}
	}
	taskID := "task-" + uuid.NewString()
	rec := &taskRecord{Task: &Task{Kind: "task", ID: taskID, ContextID: convID, Status: TaskStatus{State: TaskStateWorking, Timestamp: nowRFC3339()}}}
	h.mu.Lock()
	h.tasks[taskID] = rec
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Subscribe before sending so we don't miss anything.
	msgCh, err := h.Backend.SubscribeMessages(ctx, convID)
	if err != nil {
		writeSSEError(w, flusher, req.ID, errInternal, err.Error())
		return
	}

	// Send message in a goroutine; emit final event when done.
	done := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := h.Backend.SendMessage(ctx, convID, prompt, 5*time.Minute)
		done <- struct {
			text string
			err  error
		}{text, err}
	}()

	// Initial working event.
	writeSSEEvent(w, flusher, req.ID, TaskStatusUpdateEvent{
		Kind:      "status-update",
		TaskID:    taskID,
		ContextID: convID,
		Status:    TaskStatus{State: TaskStateWorking, Timestamp: nowRFC3339()},
	})

stream:
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-msgCh:
			if !ok {
				break stream
			}
			if chunk == "" {
				continue
			}
			writeSSEEvent(w, flusher, req.ID, TaskStatusUpdateEvent{
				Kind:      "status-update",
				TaskID:    taskID,
				ContextID: convID,
				Status: TaskStatus{
					State:     TaskStateWorking,
					Timestamp: nowRFC3339(),
					Message: &Message{
						Kind:      "message",
						MessageID: "msg-" + uuid.NewString(),
						Role:      "agent",
						Parts:     []Part{{Kind: "text", Text: chunk}},
						ContextID: convID,
						TaskID:    taskID,
					},
				},
			})
		case result := <-done:
			finalState := TaskStateCompleted
			var finalMsg *Message
			if result.err != nil {
				finalState = TaskStateFailed
				finalMsg = &Message{
					Kind:      "message",
					MessageID: "msg-" + uuid.NewString(),
					Role:      "agent",
					Parts:     []Part{{Kind: "text", Text: result.err.Error()}},
					ContextID: convID,
					TaskID:    taskID,
				}
			} else if result.text != "" {
				finalMsg = &Message{
					Kind:      "message",
					MessageID: "msg-" + uuid.NewString(),
					Role:      "agent",
					Parts:     []Part{{Kind: "text", Text: result.text}},
					ContextID: convID,
					TaskID:    taskID,
				}
			}
			writeSSEEvent(w, flusher, req.ID, TaskStatusUpdateEvent{
				Kind:      "status-update",
				TaskID:    taskID,
				ContextID: convID,
				Status:    TaskStatus{State: finalState, Message: finalMsg, Timestamp: nowRFC3339()},
				Final:     true,
			})
			return
		}
	}

	// Subscription closed before SendMessage finished — wait for completion.
	result := <-done
	finalState := TaskStateCompleted
	if result.err != nil {
		finalState = TaskStateFailed
	}
	writeSSEEvent(w, flusher, req.ID, TaskStatusUpdateEvent{
		Kind:      "status-update",
		TaskID:    taskID,
		ContextID: convID,
		Status:    TaskStatus{State: finalState, Timestamp: nowRFC3339()},
		Final:     true,
	})
}

// tasks/get ----------------------------------------------------------------

func (h *Handler) handleTaskGet(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params TaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}
	h.mu.RLock()
	rec, ok := h.tasks[params.ID]
	h.mu.RUnlock()
	if !ok {
		writeRPCError(w, req.ID, errTaskNotFound, "task not found: "+params.ID)
		return
	}
	writeRPCResult(w, req.ID, rec.Task)
}

// tasks/cancel -------------------------------------------------------------

func (h *Handler) handleTaskCancel(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params TaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, errInvalidParams, err.Error())
		return
	}
	h.mu.Lock()
	rec, ok := h.tasks[params.ID]
	h.mu.Unlock()
	if !ok {
		writeRPCError(w, req.ID, errTaskNotFound, "task not found: "+params.ID)
		return
	}
	if rec.Task.Status.State != TaskStateWorking && rec.Task.Status.State != TaskStateSubmitted {
		writeRPCError(w, req.ID, errTaskNotCancelable, "task not cancelable in state "+string(rec.Task.Status.State))
		return
	}
	if err := h.Backend.Cancel(r.Context(), rec.ConversationID); err != nil {
		writeRPCError(w, req.ID, errInternal, err.Error())
		return
	}
	h.mu.Lock()
	rec.Task.Status.State = TaskStateCanceled
	rec.Task.Status.Timestamp = nowRFC3339()
	h.mu.Unlock()
	writeRPCResult(w, req.ID, rec.Task)
}

// helpers -----------------------------------------------------------------

func promptFromMessage(m Message) (string, error) {
	if m.Role != "user" {
		return "", fmt.Errorf("message.role must be \"user\", got %q", m.Role)
	}
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Kind != "text" {
			return "", fmt.Errorf("unsupported part kind %q (v1 only supports text)", p.Kind)
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(p.Text)
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("message has no text parts")
	}
	return sb.String(), nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, id json.RawMessage, payload any) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: payload}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func writeSSEError(w http.ResponseWriter, flusher http.Flusher, id json.RawMessage, code int, msg string) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
	data, _ := json.Marshal(resp)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
