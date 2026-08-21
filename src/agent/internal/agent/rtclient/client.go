// Package rtclient implements a small, concurrency-safe client for the
// Qwen-Audio Realtime JSON-over-WebSocket API.
package rtclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultModel = "qwen-audio-3.0-realtime-plus"
	DefaultVoice = "longanqian"
)

type Config struct {
	APIKey      string
	Model       string
	WorkspaceID string
	Region      string // cn-beijing or ap-southeast-1
	Endpoint    string // useful for tests or compatible services
	UserAgent   string
	HTTPHeader  http.Header
	Dialer      *websocket.Dialer
	EventBuffer int
}

type Client struct{ cfg Config }

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("rtclient: APIKey is required")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Endpoint == "" {
		host := "dashscope.aliyuncs.com"
		switch cfg.Region {
		case "cn-beijing":
			if cfg.WorkspaceID != "" {
				host = cfg.WorkspaceID + ".cn-beijing.maas.aliyuncs.com"
			}
		case "ap-southeast-1":
			if cfg.WorkspaceID != "" {
				host = cfg.WorkspaceID + ".ap-southeast-1.maas.aliyuncs.com"
			} else {
				host = "dashscope-intl.aliyuncs.com"
			}
		case "":
		default:
			return nil, fmt.Errorf("rtclient: unsupported region %q", cfg.Region)
		}
		cfg.Endpoint = "wss://" + host + "/api-ws/v1/realtime"
	}
	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = 64
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) Connect(ctx context.Context) (*Session, error) {
	u, err := url.Parse(c.cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("rtclient: parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set("model", c.cfg.Model)
	u.RawQuery = q.Encode()
	h := make(http.Header, len(c.cfg.HTTPHeader)+2)
	for k, vv := range c.cfg.HTTPHeader {
		h[k] = append([]string(nil), vv...)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.cfg.APIKey)), "bearer ") {
		h.Set("Authorization", strings.TrimSpace(c.cfg.APIKey))
	} else {
		h.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	if c.cfg.WorkspaceID != "" {
		h.Set("X-DashScope-WorkSpace", c.cfg.WorkspaceID)
	}
	if c.cfg.UserAgent != "" {
		h.Set("User-Agent", c.cfg.UserAgent)
	}
	d := c.cfg.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	conn, resp, err := d.DialContext(ctx, u.String(), h)
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, fmt.Errorf("rtclient: websocket handshake (%s): %w", resp.Status, err)
		}
		return nil, fmt.Errorf("rtclient: connect: %w", err)
	}
	writeGate := make(chan struct{}, 1)
	writeGate <- struct{}{}
	s := &Session{conn: conn, writeGate: writeGate, events: make(chan Event, c.cfg.EventBuffer), errs: make(chan error, 1), done: make(chan struct{})}
	go s.readLoop()
	return s, nil
}

type Session struct {
	conn      *websocket.Conn
	writeGate chan struct{}
	closeOnce sync.Once
	events    chan Event
	errs      chan error
	done      chan struct{}
}

func (s *Session) Events() <-chan Event  { return s.events }
func (s *Session) Errors() <-chan error  { return s.errs }
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Send(ctx context.Context, event any) error {
	if ctx == nil {
		return errors.New("rtclient: nil context")
	}
	if event == nil {
		return errors.New("rtclient: nil event")
	}
	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("rtclient: marshal event: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("rtclient: session is closed")
	case <-s.writeGate:
	}
	defer func() { s.writeGate <- struct{}{} }()
	select {
	case <-s.done:
		return errors.New("rtclient: session is closed")
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.conn.SetWriteDeadline(deadline)
		defer s.conn.SetWriteDeadline(time.Time{})
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return fmt.Errorf("rtclient: send: %w", err)
	}
	return nil
}

func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() { close(s.done); err = s.conn.Close() })
	return err
}

func (s *Session) readLoop() {
	defer close(s.events)
	defer close(s.errs)
	defer s.closeOnce.Do(func() { close(s.done); _ = s.conn.Close() })
	for {
		_, b, err := s.conn.ReadMessage()
		if err != nil {
			if !isNormalClose(err) {
				select {
				case s.errs <- err:
				default:
				}
			}
			return
		}
		var ev Event
		if err := json.Unmarshal(b, &ev); err != nil {
			select {
			case s.errs <- fmt.Errorf("rtclient: decode event: %w", err):
			default:
			}
			continue
		}
		select {
		case s.events <- ev:
		case <-s.done:
			return
		}
	}
}

func isNormalClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, websocket.ErrCloseSent)
}

// Convenience methods mirror the client events documented by the service.
func (s *Session) Update(ctx context.Context, cfg SessionConfig) error {
	return s.Send(ctx, SessionUpdate{Type: EventSessionUpdate, Session: cfg})
}
func (s *Session) AppendAudio(ctx context.Context, audio []byte) error {
	return s.Send(ctx, InputAudioAppend{Type: EventInputAudioAppend, Audio: audio})
}
func (s *Session) CommitAudio(ctx context.Context) error {
	return s.Send(ctx, SimpleEvent{Type: EventInputAudioCommit})
}
func (s *Session) ClearAudio(ctx context.Context) error {
	return s.Send(ctx, SimpleEvent{Type: EventInputAudioClear})
}
func (s *Session) CreateItem(ctx context.Context, item ConversationItem, previousID string) error {
	return s.Send(ctx, ConversationItemCreate{Type: EventConversationItemCreate, PreviousItemID: previousID, Item: item})
}
func (s *Session) SendText(ctx context.Context, text, previousID string) error {
	return s.CreateItem(ctx, ConversationItem{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: text}}}, previousID)
}

func (s *Session) SendFunctionOutput(ctx context.Context, callID, output string) error {
	return s.CreateItem(ctx, ConversationItem{Type: "function_call_output", CallID: callID, Output: output}, "")
}
func (s *Session) RetrieveItem(ctx context.Context, itemID string) error {
	return s.Send(ctx, ConversationItemRetrieve{Type: EventConversationItemRetrieve, ItemID: itemID})
}
func (s *Session) DeleteItem(ctx context.Context, itemID string) error {
	return s.Send(ctx, ConversationItemDelete{Type: EventConversationItemDelete, ItemID: itemID})
}
func (s *Session) CreateResponse(ctx context.Context, response *ResponseCreateConfig) error {
	return s.Send(ctx, ResponseCreate{Type: EventResponseCreate, Response: response})
}
func (s *Session) CancelResponse(ctx context.Context) error {
	return s.Send(ctx, SimpleEvent{Type: EventResponseCancel})
}
