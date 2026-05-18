// Package a2a implements the Agent-to-Agent protocol server endpoints.
//
// Spec: https://a2a-protocol.org/ (JSON-RPC 2.0 over HTTP, plus SSE for streaming)
//
// This package contains protocol types and an http.Handler. It depends on a
// Backend interface implemented by the Percy server, so it stays free of any
// Percy-internal imports.
package a2a

import "encoding/json"

// JSON-RPC 2.0 envelopes ---------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes plus A2A-specific ones.
const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603

	errTaskNotFound      = -32001
	errTaskNotCancelable = -32002
	errUnsupported       = -32004
)

// A2A core types ----------------------------------------------------------

// AgentCard is the public descriptor returned from /.well-known/agent-card.json.
type AgentCard struct {
	ProtocolVersion    string             `json:"protocolVersion"`
	Name               string             `json:"name"`
	Description        string             `json:"description"`
	URL                string             `json:"url"`
	Version            string             `json:"version"`
	Capabilities       AgentCapabilities  `json:"capabilities"`
	DefaultInputModes  []string           `json:"defaultInputModes"`
	DefaultOutputModes []string           `json:"defaultOutputModes"`
	Skills             []AgentSkill       `json:"skills"`
	SecuritySchemes    map[string]any     `json:"securitySchemes,omitempty"`
	Security           []map[string][]any `json:"security,omitempty"`
}

type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
}

// Message is the unit of conversational input/output.
type Message struct {
	Kind             string   `json:"kind"` // always "message"
	MessageID        string   `json:"messageId"`
	Role             string   `json:"role"` // "user" | "agent"
	Parts            []Part   `json:"parts"`
	ContextID        string   `json:"contextId,omitempty"`
	TaskID           string   `json:"taskId,omitempty"`
	ReferenceTaskIDs []string `json:"referenceTaskIds,omitempty"`
}

// Part is a polymorphic content chunk. v1 only supports text.
type Part struct {
	Kind string `json:"kind"`           // "text" | "file" | "data"
	Text string `json:"text,omitempty"` // when kind == "text"
}

// TaskState enumerates A2A task lifecycle states.
type TaskState string

const (
	TaskStateSubmitted TaskState = "submitted"
	TaskStateWorking   TaskState = "working"
	TaskStateCompleted TaskState = "completed"
	TaskStateCanceled  TaskState = "canceled"
	TaskStateFailed    TaskState = "failed"
)

type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"`
}

// Task is the unit of work returned from message/send and tasks/get.
type Task struct {
	Kind      string     `json:"kind"` // always "task"
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
	History   []Message  `json:"history,omitempty"`
}

// MessageSendParams is the params object for the message/send and message/stream methods.
type MessageSendParams struct {
	Message       Message               `json:"message"`
	Configuration *MessageConfiguration `json:"configuration,omitempty"`
}

type MessageConfiguration struct {
	Blocking            bool     `json:"blocking,omitempty"`
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
}

type TaskIDParams struct {
	ID string `json:"id"`
}

// TaskStatusUpdateEvent is emitted on the message/stream SSE channel.
type TaskStatusUpdateEvent struct {
	Kind      string     `json:"kind"` // "status-update"
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
	Final     bool       `json:"final"`
}
