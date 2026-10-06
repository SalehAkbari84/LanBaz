package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client is a minimal control API client for loopback use by lanbazctl, the
// integration tests and (through a small bridge) the desktop shell. It is not a
// general purpose SDK: it does one authenticated session, correlated
// request/response and event delivery.
//
// The single writer rule of gorilla/websocket is respected: all writes go
// through out and a dedicated writer goroutine.
type Client struct {
	conn *websocket.Conn
	// writeMu serialises writes for callers that do not use Run.
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan Message
	closed  bool
	done    chan struct{}
	events  map[string][]func(Message)

	sessionID string
}

// DialOptions configures Dial.
type DialOptions struct {
	// Addr is the daemon API address, e.g. 127.0.0.1:51234.
	Addr string
	// Token is the value read from the daemon state file.
	Token string
	// Timeout bounds dial plus the hello handshake.
	Timeout time.Duration
	// ClientName identifies the caller in daemon logs.
	ClientName string
}

// Dial connects to a running daemon and completes the hello handshake.
func Dial(ctx context.Context, opts DialOptions) (*Client, error) {
	if opts.Addr == "" {
		return nil, NewError(CodeConfigInvalid, "daemon address is required")
	}
	if opts.Token == "" {
		return nil, NewError(CodeUnauthorized, "daemon api token is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	u := url.URL{Scheme: "ws", Host: opts.Addr, Path: APIPath}
	dialer := &websocket.Dialer{
		HandshakeTimeout: timeout,
		Proxy:            nil, // never proxy a loopback control channel
	}
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, NewErrorf(CodeInternal, "connect to %s: %v", u.String(), err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}

	c := &Client{
		conn:    conn,
		pending: make(map[string]chan Message),
		events:  make(map[string][]func(Message)),
		done:    make(chan struct{}),
	}
	conn.SetReadLimit(MaxMessageBytes)

	hello, err := NewRequest(NewRequestID(), MethodHello, HelloRequest{
		Token:   opts.Token,
		Client:  opts.ClientName,
		Version: VersionString,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.write(hello); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(timeout))
	msg, err := c.read()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !msg.IsSuccess() {
		conn.Close()
		if msg.Error != nil {
			return nil, msg.Error
		}
		return nil, NewError(CodeUnauthorized, "handshake failed")
	}
	var hr HelloResponse
	if err := msg.BindPayload(&hr); err != nil {
		conn.Close()
		return nil, err
	}
	c.sessionID = hr.SessionID
	conn.SetReadDeadline(time.Time{})

	go c.readLoop()
	return c, nil
}

// SessionID returns the id assigned by the daemon during the handshake.
func (c *Client) SessionID() string { return c.sessionID }

// Call issues a request and waits for its correlated response.
func (c *Client) Call(ctx context.Context, method string, payload any, out any) error {
	req, err := NewRequest(NewRequestID(), method, payload)
	if err != nil {
		return err
	}

	ch := make(chan Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return NewError(CodeTimeout, "client is closed")
	}
	c.pending[req.ID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
	}()

	if err := c.write(req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return NewErrorf(CodeTimeout, "request %s cancelled: %v", method, ctx.Err())
	case <-c.done:
		return NewError(CodeTimeout, "connection closed while waiting for "+method)
	case msg := <-ch:
		if !msg.IsSuccess() {
			if msg.Error != nil {
				return msg.Error
			}
			return NewError(CodeInternal, "request failed")
		}
		if out == nil {
			return nil
		}
		raw, err := json.Marshal(msg.Payload)
		if err != nil {
			return NewErrorf(CodeInternal, "cannot re-encode payload: %v", err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return NewErrorf(CodeInternal, "cannot decode response: %v", err)
		}
		return nil
	}
}

// On registers an event handler and returns an unsubscribe function.
func (c *Client) On(event string, fn func(Message)) func() {
	c.mu.Lock()
	c.events[event] = append(c.events[event], fn)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		list := c.events[event]
		for i, existing := range list {
			if fmt.Sprintf("%p", existing) == fmt.Sprintf("%p", fn) {
				c.events[event] = append(list[:i], list[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
	}
}

// Close terminates the session.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	err := c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	_ = c.conn.Close()
	<-c.done
	return err
}

func (c *Client) write(m Message) error {
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return NewErrorf(CodeTimeout, "set write deadline: %v", err)
	}
	return c.conn.WriteMessage(websocket.TextMessage, raw)
}

func (c *Client) read() (Message, error) {
	mt, raw, err := c.conn.ReadMessage()
	if err != nil {
		return Message{}, NewErrorf(CodeInternal, "read: %v", err)
	}
	if mt != websocket.TextMessage {
		return Message{}, NewError(CodeBadRequest, "unexpected binary message from daemon")
	}
	return Decode(raw)
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		mt, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		msg, err := Decode(raw)
		if err != nil {
			continue
		}
		c.dispatch(msg)
	}
}

func (c *Client) dispatch(m Message) {
	if m.Kind == KindEvent {
		c.mu.Lock()
		handlers := make([]func(Message), len(c.events[m.Type]))
		copy(handlers, c.events[m.Type])
		c.mu.Unlock()
		for _, h := range handlers {
			h(m)
		}
		return
	}

	c.mu.Lock()
	ch, ok := c.pending[m.ID]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- m:
	default:
	}
}
