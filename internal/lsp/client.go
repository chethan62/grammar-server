// Package lsp is a minimal LSP (JSON-RPC over stdio) client used to talk to harper-ls.
package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Message is a JSON-RPC 2.0 message (request, response, or notification).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *json.RawMessage `json:"error,omitempty"`
}

// Client is a blocking JSON-RPC client over a subprocess's stdio.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex // guards id counter + pending map
	nextID int64
	pending map[int64]chan Message
	// serverRequests carries server→client requests (e.g. workspace/configuration).
	ServerRequests chan Message
	Notifications  chan Message

	readerDone chan struct{}
	readErr    error
	errCh      chan error
}

// Start launches bin (e.g. harper-ls --stdio) and starts the read loop.
func Start(bin string, args ...string) (*Client, error) {
	cmd := exec.Command(bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil // let harper-ls logs go to our stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", bin, err)
	}
	c := &Client{
		cmd:            cmd,
		stdin:          stdin,
		nextID:         1,
		pending:        map[int64]chan Message{},
		ServerRequests: make(chan Message, 8),
		Notifications:  make(chan Message, 64),
		readerDone:     make(chan struct{}),
		errCh:          make(chan error, 1),
	}
	go c.readLoop(stdout)
	return c, nil
}

// Err returns a channel that receives fatal transport errors (subprocess exit).
func (c *Client) Err() <-chan error { return c.errCh }

func (c *Client) readLoop(r io.Reader) {
	defer close(c.readerDone)
	br := bufio.NewReader(r)
	for {
		// Parse Content-Length header
		clen := -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				c.readErr = fmt.Errorf("lsp read header: %w", err)
				return
			}
			line = line[:len(line)-1]
			if line == "" || line == "\r" {
				break
			}
			if len(line) > 2 && line[:len("Content-Length:")] == "Content-Length:" {
				fmt.Sscanf(line[len("Content-Length:"):], "%d", &clen)
			}
		}
		if clen < 0 {
			c.readErr = errors.New("lsp: no Content-Length header")
			return
		}
		body := make([]byte, clen)
		if _, err := io.ReadFull(br, body); err != nil {
			c.readErr = fmt.Errorf("lsp read body: %w", err)
			select {
			case c.errCh <- c.readErr:
			default:
			}
			return
		}
		var m Message
		if err := json.Unmarshal(body, &m); err != nil {
			continue // ignore malformed
		}
		switch {
		case m.ID != nil && m.Method == "":
			// response to one of our requests
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.ID != nil && m.Method != "":
			// server request (workspace/configuration, etc.)
			select {
			case c.ServerRequests <- m:
			default:
			}
		case m.Method != "":
			// notification (publishDiagnostics, ...)
			select {
			case c.Notifications <- m:
			default:
			}
		}
	}
}

// Send writes a raw message. For notifications omit id; for requests set it.
func (c *Client) Send(m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

// DefaultRequestTimeout bounds every engine round-trip. Before it existed a
// silent harper-ls held Harper.mu forever, so one bad document wedged every
// later check (observed: a 200 KB request ran past 47 s and the client's
// connection was closed while the engine kept chewing).
const DefaultRequestTimeout = 15 * time.Second

// Request sends a request and waits up to DefaultRequestTimeout.
func (c *Client) Request(method string, params any) (Message, error) {
	return c.RequestContext(method, params, DefaultRequestTimeout)
}

// RequestContext is Request with an explicit deadline.
func (c *Client) RequestContext(method string, params any, timeout time.Duration) (Message, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	pb, err := json.Marshal(params)
	if err != nil {
		c.forget(id)
		return Message{}, err
	}
	if err := c.Send(Message{JSONRPC: "2.0", ID: &id, Method: method, Params: pb}); err != nil {
		c.forget(id)
		return Message{}, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp, fmt.Errorf("lsp %s: server error: %s", method, string(*resp.Error))
		}
		return resp, nil
	case err := <-c.Err():
		c.forget(id)
		return Message{}, fmt.Errorf("lsp %s: transport: %w", method, err)
	case <-time.After(timeout):
		c.forget(id)
		return Message{}, fmt.Errorf("lsp %s: timed out after %s", method, timeout)
	}
}

// forget drops a pending entry nobody will answer.
func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Notify sends a notification (no id).
func (c *Client) Notify(method string, params any) error {
	pb, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.Send(Message{JSONRPC: "2.0", Method: method, Params: pb})
}

// Stop terminates the subprocess.
func (c *Client) Stop() {
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
}
