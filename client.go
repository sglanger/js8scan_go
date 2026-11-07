package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// APIMsg is the JS8Call API envelope (newline-delimited JSON over TCP).
type APIMsg struct {
	Type   string                 `json:"type"`
	Value  string                 `json:"value"`
	Params map[string]interface{} `json:"params"`
}

func (m APIMsg) Int(key string) int64 {
	if f, ok := m.Params[key].(float64); ok {
		return int64(f)
	}
	return 0
}

func (m APIMsg) Str(key string) string {
	if s, ok := m.Params[key].(string); ok {
		return s
	}
	return ""
}

type Client struct {
	conn   net.Conn
	mu     sync.Mutex
	Events chan APIMsg
	dial   atomic.Int64
	offset atomic.Int64
}

func Connect(host string, port int) (*Client, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, Events: make(chan APIMsg, 256)}
	go c.readLoop()
	return c, nil
}

func (c *Client) Close() { c.conn.Close() }

func (c *Client) readLoop() {
	defer close(c.Events)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var m APIMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Type == "RIG.FREQ" {
			c.dial.Store(m.Int("DIAL"))
			c.offset.Store(m.Int("OFFSET"))
		}
		select {
		case c.Events <- m:
		default: // drop if consumer is behind
		}
	}
}

func (c *Client) send(m APIMsg) error {
	if m.Params == nil {
		m.Params = map[string]interface{}{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.conn.Write(append(b, '\n'))
	return err
}

// GetFreq asks JS8Call to report the rig frequency (reply arrives as RIG.FREQ).
func (c *Client) GetFreq() error { return c.send(APIMsg{Type: "RIG.GET_FREQ"}) }

// SetFreq sets dial frequency and audio offset in Hz.
func (c *Client) SetFreq(dialHz, offsetHz int64) error {
	return c.send(APIMsg{Type: "RIG.SET_FREQ", Params: map[string]interface{}{
		"DIAL": dialHz, "OFFSET": offsetHz,
	}})
}

// SendMessage queues text for transmission (as if typed in the TX box and sent).
// TODO - add TX enabled safety check to this function
func (c *Client) SendMessage(text string) error {
	return c.send(APIMsg{Type: "TX.SEND_MESSAGE", Value: text})
}

func (c *Client) CurrentDial() int64 { return c.dial.Load() }
