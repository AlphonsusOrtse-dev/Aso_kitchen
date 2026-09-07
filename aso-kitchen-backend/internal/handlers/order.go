package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asokitchen/backend/internal/middleware"
	"github.com/asokitchen/backend/internal/models"
	"github.com/asokitchen/backend/internal/websocket"
)

type OrderHandler struct {
	DB  *pgxpool.Pool
	Hub *websocket.Hub
}

func NewOrderHandler(db *pgxpool.Pool, hub *websocket.Hub) *OrderHandler {
	return &OrderHandler{DB: db, Hub: hub}
}

// ---------- Create order (supports pre-ordering via scheduled_for) ----------

type createOrderItemInput struct {
	MenuItemID          uuid.UUID `json:"menu_item_id"`
	Quantity            int       `json:"quantity"`
	SpecialInstructions string    `json:"special_instructions,omitempty"`
}

type createOrderRequest struct {
	// NOTE: no UserID field here on purpose. Before, a client could place
	// an order as literally any user by typing their UUID into the body.
	// The real user_id now comes from the verified JWT (see CreateOrder
	// below), never from something the client can type.
	OrderType       models.OrderType       `json:"order_type"`
	ScheduledFor    *time.Time             `json:"scheduled_for,omitempty"` // nil = ASAP order
	DeliveryAddress string                 `json:"delivery_address,omitempty"`
	Notes           string                 `json:"notes,omitempty"`
	Items           []createOrderItemInput `json:"items"`
}

// POST /api/orders — requires authentication (RequireAuth middleware).
func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "order must contain at least one item")
		return
	}
	if req.OrderType == models.OrderTypeDelivery && req.DeliveryAddress == "" {
		writeError(w, http.StatusBadRequest, "delivery_address is required for delivery orders")
		return
	}

	ctx := r.Context()
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx) // no-op if committed

	orderID := uuid.New()
	var subtotal float64

	// Snapshot current prices & confirm each item is in stock.
	type resolvedItem struct {
		menuItemID uuid.UUID
		unitPrice  float64
		quantity   int
		notes      string
	}
	resolved := make([]resolvedItem, 0, len(req.Items))

	for _, it := range req.Items {
		var price float64
		var available bool
		err := tx.QueryRow(ctx,
			`SELECT price, is_available FROM menu_items WHERE id = $1 FOR UPDATE`,
			it.MenuItemID,
		).Scan(&price, &available)
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusBadRequest, "menu item not found: "+it.MenuItemID.String())
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to look up menu item")
			return
		}
		if !available {
			writeError(w, http.StatusConflict, "item is out of stock: "+it.MenuItemID.String())
			return
		}

		lineTotal := price * float64(it.Quantity)
		subtotal += lineTotal
		resolved = append(resolved, resolvedItem{
			menuItemID: it.MenuItemID,
			unitPrice:  price,
			quantity:   it.Quantity,
			notes:      it.SpecialInstructions,
		})
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (id, user_id, order_type, status, scheduled_for,
		                     delivery_address, subtotal_amount, total_amount, notes)
		VALUES ($1, $2, $3, 'pending', $4, $5, $6, $6, $7)`,
		orderID, userID, req.OrderType, req.ScheduledFor,
		req.DeliveryAddress, subtotal, req.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create order")
		return
	}

	for _, it := range resolved {
		_, err = tx.Exec(ctx, `
			INSERT INTO order_items (id, order_id, menu_item_id, quantity, unit_price, subtotal, special_instructions)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uuid.New(), orderID, it.menuItemID, it.quantity, it.unitPrice,
			it.unitPrice*float64(it.quantity), it.notes)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add order item")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit order")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":     orderID,
		"status": models.StatusPending,
	})
}

// ---------- Get order ----------

// GET /api/orders/{orderID}
func (h *OrderHandler) GetOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	ctx := r.Context()
	var o models.Order
	err = h.DB.QueryRow(ctx, `
		SELECT id, user_id, order_type, status, scheduled_for, delivery_address,
		       subtotal_amount, total_amount, payment_status, notes, created_at, updated_at
		FROM orders WHERE id = $1`, orderID,
	).Scan(&o.ID, &o.UserID, &o.OrderType, &o.Status, &o.ScheduledFor, &o.DeliveryAddress,
		&o.SubtotalAmount, &o.TotalAmount, &o.PaymentStatus, &o.Notes, &o.CreatedAt, &o.UpdatedAt)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "order not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load order")
		return
	}

	rows, err := h.DB.Query(ctx, `
		SELECT oi.id, oi.menu_item_id, mi.name, oi.quantity, oi.unit_price, oi.subtotal, oi.special_instructions
		FROM order_items oi
		JOIN menu_items mi ON mi.id = oi.menu_item_id
		WHERE oi.order_id = $1`, orderID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var it models.OrderItem
			if err := rows.Scan(&it.ID, &it.MenuItemID, &it.Name, &it.Quantity,
				&it.UnitPrice, &it.Subtotal, &it.SpecialInstructions); err == nil {
				o.Items = append(o.Items, it)
			}
		}
	}

	writeJSON(w, http.StatusOK, o)
}

// ---------- Update order status (kitchen-side action) ----------

type updateStatusRequest struct {
	Status models.OrderStatus `json:"status"`
	Note   string             `json:"note,omitempty"`
}

// PATCH /api/orders/{orderID}/status
// This is the endpoint the kitchen staff dashboard calls as food moves
// through Pending -> Confirmed -> Preparing -> Cooking -> Ready -> Fulfilled.
// On success, it broadcasts the new status to every customer client
// currently subscribed to this order over WebSocket.
func (h *OrderHandler) UpdateOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	var req updateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()

	var currentStatus models.OrderStatus
	if err := h.DB.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).
		Scan(&currentStatus); err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "order not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load order")
		return
	}

	if !currentStatus.CanTransitionTo(req.Status) {
		writeError(w, http.StatusConflict,
			"cannot transition from "+string(currentStatus)+" to "+string(req.Status))
		return
	}

	_, err = h.DB.Exec(ctx, `UPDATE orders SET status = $1 WHERE id = $2`, req.Status, orderID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update status")
		return
	}

	// Optional: attach a human note to the auto-generated history row.
	if req.Note != "" {
		_, _ = h.DB.Exec(ctx, `
			UPDATE order_status_history
			SET note = $1
			WHERE id = (SELECT id FROM order_status_history WHERE order_id = $2 ORDER BY changed_at DESC LIMIT 1)`,
			req.Note, orderID)
	}

	// Push the update live to any subscribed customer.
	h.Hub.Broadcast(models.OrderStatusEvent{
		OrderID:   orderID,
		Status:    req.Status,
		Note:      req.Note,
		Timestamp: time.Now(),
	})

	writeJSON(w, http.StatusOK, map[string]any{"id": orderID, "status": req.Status})
}

// ---------- WebSocket subscription ----------

// GET /ws/orders/{orderID}
// Customer's frontend opens this to receive live status push events for
// their own order (drives the progress bar).
func (h *OrderHandler) SubscribeOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	h.Hub.ServeOrderWS(w, r, orderID)
}
