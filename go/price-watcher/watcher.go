package main

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// Constants for the WebSocket connection
const (
	wsURL = "wss://ws3.indodax.com/ws/"
	// The provided JWT token
	authToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE5NDY2MTg0MTV9.UR1lBM6Eqh0yWz-PVirw1uPCxe60FdchR8eNVdsskeo"
	// The pair we want to subscribe to
	pair = "usdtidr"
)

// --- Structs for WebSocket Messages ---

// AuthRequest is used to authenticate the WebSocket connection.
type AuthRequest struct {
	Params map[string]string `json:"params"`
	ID     int               `json:"id"`
}

// SubscribeRequest is used to subscribe to a specific channel.
type SubscribeRequest struct {
	Method int               `json:"method"`
	Params map[string]string `json:"params"`
	ID     int               `json:"id"`
}

// PongMessage is sent in response to a server Ping.
type PongMessage struct {
	Method string `json:"method"`
}

// GenericResponse is used to parse incoming messages to determine their type.
// We use json.RawMessage to delay parsing of nested data until we know its structure.
type GenericResponse struct {
	ID     int             `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Method string          `json:"method,omitempty"`
	Error  interface{}     `json:"error,omitempty"`
}

// ResultData contains channel information.
type ResultData struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

// Trade represents a single trade event from the 'trade-activity' channel.
type Trade struct {
	Data   [][]interface{} `json:"data"` // [pair, timestamp, seq, side, price, idr_vol, coin_vol]
	Offset int             `json:"offset"`
}

// OrderBook represents the data from the 'order-book' channel.
type OrderBook struct {
	Data struct {
		Pair string       `json:"pair"`
		Ask  []OrderEntry `json:"ask"`
		Bid  []OrderEntry `json:"bid"`
	} `json:"data"`
	Offset int `json:"offset"`
}

// OrderEntry is a single entry in the order book (either ask or bid).
type OrderEntry struct {
	CoinVolume string `json:"usdt_volume"` // Note: The key changes based on the pair, e.g., btc_volume
	IDRVolume  string `json:"idr_volume"`
	Price      string `json:"price"`
}

func init() {
	// Load environment variables from ../.env file
	if err := godotenv.Load("../../.env"); err != nil { // Specify the path to the .env file
		log.Println("Error loading ../../.env file:", err)
		log.Println("Attempting to use system environment variables or default values.")
	}

	// Read the environment variables
	indodaxWS := os.Getenv("INDODAX_LAST_PRICE_WS")
	pintuWS := os.Getenv("PINTU_LAST_PRICE_WS")
	log.Printf("INDODAX_LAST_PRICE_WS: %s", indodaxWS)
	log.Printf("PINTU_LAST_PRICE_WS: %s", pintuWS)

}

// --- Main Application Logic ---

func main() {
	// Set up a channel to handle OS interrupt signals (like Ctrl+C)
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Parse the WebSocket URL
	u, err := url.Parse(wsURL)
	if err != nil {
		log.Fatalf("url.Parse: %v", err)
	}
	log.Printf("Connecting to %s", u.String())

	// Dial the WebSocket server
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("Dial error: %v", err)
	}
	defer c.Close()
	log.Println("✅ Successfully connected to Indodax WebSocket.")

	// Create a channel to signal when the read loop is done
	done := make(chan struct{})

	// Start a goroutine to read messages from the WebSocket
	go readMessages(c, done)

	// 1. Authenticate
	authenticate(c)

	// Small delay to ensure authentication is processed before subscribing
	time.Sleep(1 * time.Second)

	// 2. Subscribe to channels
	subscribeToChannels(c)

	// Main loop to wait for either an interrupt or the read loop to finish
	for {
		select {
		case <-done:
			log.Println("Read loop finished. Exiting.")
			return
		case <-interrupt:
			log.Println("Interrupt received, closing connection.")
			// Attempt a clean shutdown by sending a close message
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("Write close error:", err)
				return
			}
			// Wait for the server to close the connection or timeout
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
	}
}

// readMessages continuously reads from the WebSocket connection.
func readMessages(c *websocket.Conn, done chan struct{}) {
	defer close(done) // Signal that this goroutine has finished
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			return // Exit loop on error
		}

		// First, check if it's a ping from the server
		var genericMsg GenericResponse
		if err := json.Unmarshal(message, &genericMsg); err == nil && genericMsg.Method == "ping" {
			log.Println("🏓 Ping received, sending Pong...")
			pong := PongMessage{Method: "pong"}
			if err := c.WriteJSON(pong); err != nil {
				log.Println("Write pong error:", err)
				return
			}
			continue
		}

		// If not a ping, process as a regular message
		processData(message)
	}
}

// processData unmarshals and handles the different types of data messages.
func processData(message []byte) {
	var genericResponse GenericResponse
	if err := json.Unmarshal(message, &genericResponse); err != nil {
		log.Printf("Cannot unmarshal generic response: %v", err)
		return
	}

	// Handle authentication and subscription confirmation messages
	if genericResponse.ID != 0 {
		log.Printf("📬 Received confirmation for request ID %d: %s", genericResponse.ID, string(genericResponse.Result))
		return
	}

	// Handle streaming data from channels
	if genericResponse.Result != nil {
		var resultData ResultData
		if err := json.Unmarshal(genericResponse.Result, &resultData); err != nil {
			log.Printf("Cannot unmarshal result data: %v", err)
			return
		}

		switch resultData.Channel {
		case "market:trade-activity-" + pair:
			var tradeData Trade
			if err := json.Unmarshal(resultData.Data, &tradeData); err == nil {
				log.Printf("📈 Trade Activity (%s): %+v", resultData.Channel, tradeData.Data)
			}
		case "market:order-book-" + pair:
			var orderBook OrderBook
			if err := json.Unmarshal(resultData.Data, &orderBook); err == nil {
				log.Printf("📚 Order Book (%s): Asks: %d, Bids: %d", resultData.Channel, len(orderBook.Data.Ask), len(orderBook.Data.Bid))
			}
		default:
			log.Printf("Received data from unhandled channel '%s'", resultData.Channel)
		}
	}
}

// authenticate sends the authentication request to the WebSocket.
func authenticate(c *websocket.Conn) {
	authReq := AuthRequest{
		Params: map[string]string{"token": authToken},
		ID:     1,
	}
	log.Println("🔐 Sending authentication request...")
	if err := c.WriteJSON(authReq); err != nil {
		log.Fatalf("Authentication failed: %v", err)
	}
}

// subscribeToChannels sends subscription requests for the required channels.
func subscribeToChannels(c *websocket.Conn) {
	// Subscribe to Trade Activity
	subTrade := SubscribeRequest{
		Method: 1,
		Params: map[string]string{"channel": "market:trade-activity-" + pair},
		ID:     2,
	}
	log.Printf("📩 Subscribing to %s...", subTrade.Params["channel"])
	if err := c.WriteJSON(subTrade); err != nil {
		log.Printf("Failed to subscribe to trade activity: %v", err)
	}

	// Subscribe to Order Book
	subOrderBook := SubscribeRequest{
		Method: 1,
		Params: map[string]string{"channel": "market:order-book-" + pair},
		ID:     3,
	}
	log.Printf("📩 Subscribing to %s...", subOrderBook.Params["channel"])
	if err := c.WriteJSON(subOrderBook); err != nil {
		log.Printf("Failed to subscribe to order book: %v", err)
	}
}
