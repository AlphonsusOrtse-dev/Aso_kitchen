package models

import (
	"time"

	"github.com/google/uuid"
)

// OrderStatus mirrors the Postgres `order_status` enum.
// The ordering here is what drives the front-end progress bar.
type OrderStatus string

const (
	StatusPending   OrderStatus = "pending"
	StatusConfirmed OrderStatus = "confirmed"
	StatusPreparing OrderStatus = "preparing"
	StatusCooking   OrderStatus = "cooking"
	StatusReady     OrderStatus = "ready"
	StatusFulfilled OrderStatus = "fulfilled"
	StatusCancelled OrderStatus = "cancelled"
)

// validNextStatus defines the allowed forward transitions for an order.
var validNextStatus = map[OrderStatus][]OrderStatus{
	StatusPending:   {StatusConfirmed, StatusCancelled},
	StatusConfirmed: {StatusPreparing, StatusCancelled},
	StatusPreparing: {StatusCooking, StatusCancelled},
	StatusCooking:   {StatusReady, StatusCancelled},
	StatusReady:     {StatusFulfilled, StatusCancelled},
	StatusFulfilled: {},
	StatusCancelled: {},
}

func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	for _, allowed := range validNextStatus[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

type OrderType string

const (
	OrderTypePickup   OrderType = "pickup"
	OrderTypeDelivery OrderType = "delivery"
)

type User struct {
	ID        uuid.UUID `json:"id"`
	FullName  string    `json:"full_name"`
	Email     string    `json:"email"`
	Phone     *string   `json:"phone,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type MenuItem struct {
	ID            uuid.UUID `json:"id"`
	CategoryID    *int      `json:"category_id,omitempty"`
	Name          string    `json:"name"`
	Description   *string   `json:"description,omitempty"`
	Price         float64   `json:"price"`
	ImageURL      *string   `json:"image_url,omitempty"`
	IsAvailable   bool      `json:"is_available"`
	StockQuantity int       `json:"stock_quantity"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type OrderItem struct {
	ID                  uuid.UUID `json:"id"`
	MenuItemID          uuid.UUID `json:"menu_item_id"`
	Name                string    `json:"name,omitempty"` // populated on read for convenience
	Quantity            int       `json:"quantity"`
	UnitPrice           float64   `json:"unit_price"`
	Subtotal            float64   `json:"subtotal"`
	SpecialInstructions *string   `json:"special_instructions,omitempty"`
}

type Order struct {
	ID              uuid.UUID   `json:"id"`
	UserID          uuid.UUID   `json:"user_id"`
	OrderType       OrderType   `json:"order_type"`
	Status          OrderStatus `json:"status"`
	ScheduledFor    *time.Time  `json:"scheduled_for,omitempty"`
	DeliveryAddress *string     `json:"delivery_address,omitempty"`
	SubtotalAmount  float64     `json:"subtotal_amount"`
	TotalAmount     float64     `json:"total_amount"`
	PaymentStatus   string      `json:"payment_status"`
	Notes           *string     `json:"notes,omitempty"`
	Items           []OrderItem `json:"items,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// OrderStatusEvent is what gets pushed down the WebSocket to subscribers
// of a given order.
type OrderStatusEvent struct {
	OrderID   uuid.UUID   `json:"order_id"`
	Status    OrderStatus `json:"status"`
	Note      string      `json:"note,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}