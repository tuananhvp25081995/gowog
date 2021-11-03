package ws

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"time"

	"github.com/giongto35/gowog/server/game/gameconst"
	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// Client is a middleman between the websocket connection and the hub.
type clientImpl struct {
	hub Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte

	// ID
	id int32

	// RemoteAddress
	remoteAdd net.Addr
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *clientImpl) ReadPump() {
	defer func() {
		c.hub.UnRegister(c)
		c.conn.Close()
	}()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			// Client disconnect
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		c.hub.ReceiveMessage(message)
	}
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *clientImpl) WritePump() {
	defer func() {
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			// NOTE: if there is remaining in send, will cause deadlock
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				fmt.Println("Write pump closed", c.GetID())
				return
			}

			w, err := c.conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				fmt.Println("Write pump closed ", c.GetID(), err)
				return
			}
			w.Write(message)

			// Add queued chat messages to the current websocket message.
			if err := w.Close(); err != nil {
				fmt.Println("Write pump cannot closed", c.GetID(), err)
				return
			}
		}
	}
}

func (c *clientImpl) Close() {
	close(c.send)
}

// Send pushes message event to channel, so it can be processed concurrently
func (c *clientImpl) Send(message []byte) {
	c.send <- message
}

// GetSend returns Send channel
func (c *clientImpl) GetSend() chan []byte {
	return c.send
}

// GetID returns client ID
func (c *clientImpl) GetID() int32 {
	return c.id
}

// NewClient returns new client given hub
func NewClient(conn *websocket.Conn, hub Hub) Client {
	// Check if the remote address is reused (there are more than one client)

	// TODO: disconnect and reconnect cause deadlock
	client := &clientImpl{id: rand.Int31(), hub: hub, conn: conn, send: make(chan []byte, gameconst.BufferSize)}

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	return client
}
