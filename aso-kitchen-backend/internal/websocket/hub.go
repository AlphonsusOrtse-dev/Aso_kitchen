package websocket

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/asokitchen/backend/internal/models"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// NOTE: tighten this for production — restrict to your known frontend origins.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// client represents a single connected browser/mobile socket watching one order.
type client struct {
	conn    *websocket.Conn
	send    chan models.OrderStatusEvent
	orderID uuid.UUID
}

// Hub fans out order status events to every client currently watching
// that order. One Hub instance is shared app-wide.
//
// This is intentionally simple (in-process, in-memory). If you run more
// than one backend instance behind a load balancer, replace the internal
// broadcast with a Redis Pub/Sub channel (see README) so events propagate
// across instances.
type Hub struct {
	mu       sync.RWMutex
	watchers map[uuid.UUID]map[*client]bool // orderID -> set of clients
}

func NewHub() *Hub {
	return &Hub{
		watchers: make(map[uuid.UUID]map[*client]bool),
	}
}

// ServeOrderWS upgrades the HTTP connection and subscribes it to updates
// for a single order ID.
func (h *Hub) ServeOrderWS(w http.ResponseWriter, r *http.Request, orderID uuid.UUID) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	c := &client{
		conn:    conn,
		send:    make(chan models.OrderStatusEvent, 8),
		orderID: orderID,
	}

	h.register(c)
	defer h.unregister(c)

	go c.writePump()
	c.readPump() // blocks until client disconnects
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchers[c.orderID] == nil {
		h.watchers[c.orderID] = make(map[*client]bool)
	}
	h.watchers[c.orderID][c] = true
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if clients, ok := h.watchers[c.orderID]; ok {
		delete(clients, c)
		if len(clients) == 0 {
			delete(h.watchers, c.orderID)
		}
	}
	close(c.send)
	c.conn.Close()
}

// Broadcast pushes a status event to every client currently watching that order.
// Call this from the "update order status" handler after the DB write succeeds.
func (h *Hub) Broadcast(event models.OrderStatusEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.watchers[event.OrderID] {
		select {
		case c.send <- event:
		default:
			// client's buffer is full/slow — drop rather than block the broadcaster
			log.Printf("dropping ws event for order %s: client buffer full", event.OrderID)
		}
	}
}

func (c *client) writePump() {
	for event := range c.send {
		data, err := json.Marshal(event)
		if err != nil {
			log.Printf("ws marshal error: %v", err)
			continue
		}
		if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

// readPump exists mainly to detect client disconnects (and to respond to
// pings). We don't expect the client to send meaningful data.
func (c *client) readPump() {
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
