package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Hook names. User hooks are executable scripts placed at
// $HOME/.config/percy/hooks/<name>. See HOOKS.md.
const (
	hookSystemPrompt    = "system-prompt"
	hookNewConversation = "new-conversation"
	hookEndOfTurn       = "end-of-turn"
	hookChatMessage     = "chat-message"
)

// hookTimeout bounds how long a user hook may run before it is killed.
const hookTimeout = 30 * time.Second

// defaultHooksDir is $HOME/.config/percy/hooks, or "" if $HOME is not set.
// Resolved on each call so that, e.g., a test that swaps $HOME locally still
// sees its change.
func defaultHooksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "percy", "hooks")
}

// findHookIn returns the path to the named hook inside dir if it exists and is
// executable, or "" if not found. An empty dir means hooks are disabled.
func findHookIn(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid hook name: %q", name)
	}
	if dir == "" {
		return "", nil
	}
	hookPath := filepath.Join(dir, name)
	info, err := os.Stat(hookPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", nil
	}
	return hookPath, nil
}

// runHookRaw runs the named hook with stdin, returning trimmed stdout. If the
// hook is not installed, ok is false and err is nil.
func runHookIn(dir, name, stdin string) (out string, ok bool, err error) {
	hookPath, err := findHookIn(dir, name)
	if err != nil {
		return "", false, fmt.Errorf("%s hook: %w", name, err)
	}
	if hookPath == "" {
		return "", false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", true, fmt.Errorf("%s hook %s failed: %w (stderr: %s)", name, hookPath, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), true, nil
}

// HookHeaders converts an http.Header to a sorted list of [name, value] pairs
// used in hook JSON payloads, stripping headers that routinely carry auth
// secrets. Multi-valued headers produce one pair per value. Returns nil if no
// headers remain so `omitempty` drops the field for non-HTTP callers.
func HookHeaders(h http.Header) [][2]string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for k := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Cookie", "Set-Cookie", "Authorization", "Proxy-Authorization":
			continue
		}
		names = append(names, k)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := make([][2]string, 0, len(names))
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// RunSystemPromptHookIn passes the rendered system prompt through the
// system-prompt hook. The hook's stdout replaces the prompt; a non-empty
// result is required. If no hook is installed, the prompt is returned
// unchanged. A non-nil error means the hook failed and the caller should
// abort.
func RunSystemPromptHookIn(hooksDir, prompt string) (string, error) {
	out, ok, err := runHookIn(hooksDir, hookSystemPrompt, prompt)
	if err != nil {
		return prompt, err
	}
	if !ok {
		return prompt, nil
	}
	if out == "" {
		return prompt, fmt.Errorf("system-prompt hook returned empty output")
	}
	slog.Info("system-prompt hook applied", "originalLen", len(prompt), "newLen", len(out))
	return out, nil
}

// NewConversationHookInput is the JSON passed to the new-conversation hook on
// stdin. Top-level fields are mutable; Readonly is context only.
type NewConversationHookInput struct {
	Prompt   string                  `json:"prompt"`
	Model    string                  `json:"model"`
	Cwd      string                  `json:"cwd"`
	Readonly NewConversationReadonly `json:"readonly"`
}

// NewConversationReadonly contains fields the hook can read but not change.
type NewConversationReadonly struct {
	ConversationID string      `json:"conversation_id"`
	IsSubagent     bool        `json:"is_subagent"`
	ParentID       string      `json:"parent_id,omitempty"`
	Headers        [][2]string `json:"headers,omitempty"`
}

// NewConversationHookResult holds the (possibly modified) mutable fields.
type NewConversationHookResult struct {
	Prompt string
	Model  string
	Cwd    string
	Slug   string
}

// RunNewConversationHookIn runs the new-conversation hook. A non-nil error
// means the hook failed and the caller should abort. If no hook is installed,
// the input values are returned unchanged with a nil error.
func RunNewConversationHookIn(hooksDir string, input NewConversationHookInput) (NewConversationHookResult, error) {
	original := NewConversationHookResult{Prompt: input.Prompt, Model: input.Model, Cwd: input.Cwd}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return original, fmt.Errorf("new-conversation hook: marshal input: %w", err)
	}
	out, ok, err := runHookIn(hooksDir, hookNewConversation, string(inputJSON))
	if err != nil {
		return original, err
	}
	if !ok || out == "" {
		return original, nil
	}

	var hookOut struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
		Cwd    string `json:"cwd"`
		Slug   string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(out), &hookOut); err != nil {
		return original, fmt.Errorf("new-conversation hook: invalid JSON output %q: %w", out, err)
	}

	result := original
	if hookOut.Cwd != "" {
		result.Cwd = hookOut.Cwd
	}
	if hookOut.Prompt != "" {
		result.Prompt = hookOut.Prompt
	}
	if hookOut.Model != "" {
		result.Model = hookOut.Model
	}
	if hookOut.Slug != "" {
		result.Slug = hookOut.Slug
	}
	if result != original {
		slog.Info("new-conversation hook applied overrides",
			"cwdChanged", result.Cwd != original.Cwd,
			"promptChanged", result.Prompt != original.Prompt,
			"modelChanged", result.Model != original.Model,
			"slugChanged", result.Slug != original.Slug)
	}
	return result, nil
}

// ChatMessageHookInput is the JSON passed to the chat-message hook on stdin.
// It fires for follow-up messages to an existing conversation (the first
// message of a new conversation uses the new-conversation hook instead).
type ChatMessageHookInput struct {
	Message  string              `json:"message"`
	Readonly ChatMessageReadonly `json:"readonly"`
}

// ChatMessageReadonly is the readonly context for the chat-message hook.
type ChatMessageReadonly struct {
	ConversationID string      `json:"conversation_id"`
	Model          string      `json:"model"`
	Headers        [][2]string `json:"headers,omitempty"`
}

// RunChatMessageHookIn runs the chat-message hook. On success with non-empty
// JSON stdout {"message": ...} the hook output replaces the user message. A
// non-nil error means the hook failed and the caller should abort. If no hook
// is installed, the input message is returned unchanged.
func RunChatMessageHookIn(hooksDir string, input ChatMessageHookInput) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return input.Message, fmt.Errorf("chat-message hook: marshal input: %w", err)
	}
	out, ok, err := runHookIn(hooksDir, hookChatMessage, string(inputJSON))
	if err != nil {
		return input.Message, err
	}
	if !ok || out == "" {
		return input.Message, nil
	}

	var hookOut struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &hookOut); err != nil {
		return input.Message, fmt.Errorf("chat-message hook: invalid JSON output %q: %w", out, err)
	}
	if hookOut.Message == "" || hookOut.Message == input.Message {
		return input.Message, nil
	}
	slog.Info("chat-message hook applied override", "conversationID", input.Readonly.ConversationID)
	return hookOut.Message, nil
}

// EndOfTurnHookInput is the JSON passed to the end-of-turn hook on stdin. It
// mirrors the notifications.Event shape that drives end-of-turn notifications.
type EndOfTurnHookInput struct {
	Type            string    `json:"type"`
	ConversationID  string    `json:"conversation_id"`
	Timestamp       time.Time `json:"timestamp"`
	Model           string    `json:"model,omitempty"`
	Slug            string    `json:"slug,omitempty"`
	ConversationURL string    `json:"conversation_url,omitempty"`
	FinalResponse   string    `json:"final_response,omitempty"`
}

// RunEndOfTurnHookIn fires the end-of-turn hook with the event JSON on stdin
// and ignores stdout. A non-nil error means the hook failed; because the turn
// is already over there is nothing to abort, so callers just log it.
func RunEndOfTurnHookIn(hooksDir string, input EndOfTurnHookInput) error {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("end-of-turn hook: marshal input: %w", err)
	}
	_, ok, err := runHookIn(hooksDir, hookEndOfTurn, string(inputJSON))
	if err != nil {
		return err
	}
	if ok {
		slog.Info("end-of-turn hook applied", "conversationID", input.ConversationID)
	}
	return nil
}
