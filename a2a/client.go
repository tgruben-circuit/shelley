package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Client is an A2A protocol client. The BaseURL should point at the remote
// agent's root (e.g. "http://localhost:9000"); GetCard hits BaseURL +
// /.well-known/agent-card.json and RPC calls hit BaseURL + /a2a.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTPClient: http.DefaultClient}
}

// GetCard fetches the remote agent's AgentCard.
func (c *Client) GetCard(ctx context.Context) (*AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/.well-known/agent-card.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent card: %s: %s", resp.Status, body)
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// SendMessage performs a blocking message/send call. contextID may be empty to
// start a new conversation; the returned Task includes the assigned ContextID.
func (c *Client) SendMessage(ctx context.Context, contextID, text string) (*Task, error) {
	msg := Message{
		Kind:      "message",
		MessageID: uuid.NewString(),
		Role:      "user",
		Parts:     []Part{{Kind: "text", Text: text}},
		ContextID: contextID,
	}
	params := MessageSendParams{Message: msg}
	var task Task
	if err := c.rpc(ctx, "message/send", params, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// StreamMessage performs a message/stream call and yields events until the
// terminal (final) event arrives or ctx is canceled.
func (c *Client) StreamMessage(ctx context.Context, contextID, text string) (<-chan TaskStatusUpdateEvent, <-chan error) {
	events := make(chan TaskStatusUpdateEvent, 8)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)

		msg := Message{
			Kind:      "message",
			MessageID: uuid.NewString(),
			Role:      "user",
			Parts:     []Part{{Kind: "text", Text: text}},
			ContextID: contextID,
		}
		body, _ := json.Marshal(rpcRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`"1"`),
			Method:  "message/stream",
			Params:  marshalParams(MessageSendParams{Message: msg}),
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/a2a", bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		c.setHeaders(req)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			errs <- fmt.Errorf("stream: %s: %s", resp.Status, b)
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var env rpcResponse
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err != nil {
				errs <- err
				return
			}
			if env.Error != nil {
				errs <- fmt.Errorf("a2a error %d: %s", env.Error.Code, env.Error.Message)
				return
			}
			resultBytes, err := json.Marshal(env.Result)
			if err != nil {
				errs <- err
				return
			}
			var ev TaskStatusUpdateEvent
			if err := json.Unmarshal(resultBytes, &ev); err != nil {
				errs <- err
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			if ev.Final {
				return
			}
		}
		if err := sc.Err(); err != nil {
			errs <- err
		}
	}()
	return events, errs
}

// GetTask polls a task by ID.
func (c *Client) GetTask(ctx context.Context, taskID string) (*Task, error) {
	var task Task
	if err := c.rpc(ctx, "tasks/get", TaskIDParams{ID: taskID}, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// CancelTask cancels a task.
func (c *Client) CancelTask(ctx context.Context, taskID string) (*Task, error) {
	var task Task
	if err := c.rpc(ctx, "tasks/cancel", TaskIDParams{ID: taskID}, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (c *Client) rpc(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"1"`),
		Method:  method,
		Params:  marshalParams(params),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/a2a", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s: %s", method, resp.Status, b)
	}
	var env rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("a2a error %d: %s", env.Error.Code, env.Error.Message)
	}
	resultBytes, err := json.Marshal(env.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(resultBytes, out)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func marshalParams(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ExtractAgentText pulls the final agent text out of a completed Task.
func ExtractAgentText(t *Task) string {
	if t == nil || t.Status.Message == nil {
		return ""
	}
	var parts []string
	for _, p := range t.Status.Message.Parts {
		if p.Kind == "text" && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}
