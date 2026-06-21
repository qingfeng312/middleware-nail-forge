package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tent-of-trials/market/matching"
	"github.com/tent-of-trials/market/types"
	"go.uber.org/zap"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	subs   map[subscriptionKey]struct{}
	remote string
	mu     sync.Mutex
}

type subscriptionKey struct {
	Channel string
	Symbol  types.Symbol
}

type subscriptionMessage struct {
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
}

type socketError struct {
	Type    string            `json:"type"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type subscriptionAck struct {
	Type    string       `json:"type"`
	Channel string       `json:"channel"`
	Symbol  types.Symbol `json:"symbol"`
}

var marketSymbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,16}-[A-Z0-9]{2,16}$`)

type Hub struct {
	clients    map[*Client]struct{}
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
	logger     *zap.Logger
	mu         sync.RWMutex
}

type Server struct {
	hub    *Hub
	engine *matching.MatchingEngine
	logger *zap.Logger
	port   int
	srv    *http.Server
}

func NewHub(logger *zap.Logger) *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
		logger:     logger,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			h.logger.Info("client connected",
				zap.String("remote", client.remote),
				zap.Int("total", len(h.clients)),
			)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			h.logger.Info("client disconnected",
				zap.String("remote", client.remote),
				zap.Int("total", len(h.clients)),
			)

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func NewServer(hub *Hub, engine *matching.MatchingEngine, logger *zap.Logger, port int) *Server {
	return &Server{
		hub:    hub,
		engine: engine,
		logger: logger,
		port:   port,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/trades", s.handleGetTrades)
	mux.HandleFunc("/api/v1/depth", s.handleGetDepth)

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s.srv.ListenAndServe()
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.srv.Shutdown(ctx)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("websocket upgrade failed", zap.Error(err))
		return
	}

	client := &Client{
		hub:    s.hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		subs:   make(map[subscriptionKey]struct{}),
		remote: r.RemoteAddr,
	}

	s.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"service": "tent-market",
		"time":    time.Now().Unix(),
	})
}

func (s *Server) handleGetTrades(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	trades := s.engine.GetRecentTrades(100)
	json.NewEncoder(w).Encode(trades)
}

func (s *Server) handleGetDepth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "depth endpoint"})
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(65536)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		c.handleInboundMessage(message)
	}
}

func (c *Client) handleInboundMessage(message []byte) {
	var event subscriptionMessage
	if err := json.Unmarshal(message, &event); err != nil {
		c.sendSocketError("invalid_json", "message must be valid JSON", nil)
		return
	}

	switch event.Type {
	case "subscribe":
		c.handleSubscribe(event)
	default:
		c.sendSocketError("unknown_message_type", "unknown websocket message type", map[string]string{
			"type": event.Type,
		})
	}
}

func (c *Client) handleSubscribe(event subscriptionMessage) {
	channel := strings.TrimSpace(event.Channel)
	if channel == "" {
		c.sendSocketError("missing_channel", "subscription channel is required", nil)
		return
	}

	symbol, err := normalizeMarketSymbol(event.Symbol)
	if err != nil {
		c.sendSocketError("invalid_symbol", err.Error(), map[string]string{
			"symbol": event.Symbol,
			"format": "BASE-QUOTE",
		})
		return
	}

	key := subscriptionKey{Channel: channel, Symbol: symbol}
	c.mu.Lock()
	if _, exists := c.subs[key]; exists {
		c.mu.Unlock()
		c.sendSocketError("duplicate_subscription", "subscription already exists for this connection", map[string]string{
			"channel": channel,
			"symbol":  string(symbol),
		})
		return
	}
	c.subs[key] = struct{}{}
	c.mu.Unlock()

	c.sendJSON(subscriptionAck{
		Type:    "subscribed",
		Channel: channel,
		Symbol:  symbol,
	})
}

func normalizeMarketSymbol(raw string) (types.Symbol, error) {
	symbol := strings.ToUpper(strings.TrimSpace(raw))
	symbol = strings.ReplaceAll(symbol, "/", "-")
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if !marketSymbolPattern.MatchString(symbol) {
		return "", fmt.Errorf("symbol must use BASE-QUOTE format")
	}

	parts := strings.Split(symbol, "-")
	if len(parts) != 2 || parts[0] == parts[1] {
		return "", fmt.Errorf("symbol must contain distinct base and quote assets")
	}

	return types.Symbol(symbol), nil
}

func (c *Client) sendSocketError(code, message string, details map[string]string) {
	c.sendJSON(socketError{
		Type:    "error",
		Code:    code,
		Message: message,
		Details: details,
	})
}

func (c *Client) sendJSON(v interface{}) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}

	select {
	case c.send <- payload:
	default:
		if c.hub != nil && c.hub.logger != nil {
			c.hub.logger.Warn("dropping websocket response for slow client",
				zap.String("remote", c.remote),
			)
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
