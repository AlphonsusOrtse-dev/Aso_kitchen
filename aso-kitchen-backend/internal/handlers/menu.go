package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asokitchen/backend/internal/models"
)

type MenuHandler struct {
	DB *pgxpool.Pool
}

func NewMenuHandler(db *pgxpool.Pool) *MenuHandler {
	return &MenuHandler{DB: db}
}

// GET /api/menu — public, browsed by customers.
func (h *MenuHandler) ListMenu(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.DB.Query(ctx, `
		SELECT id, category_id, name, description, price, image_url,
		       is_available, stock_quantity, updated_at
		FROM menu_items
		ORDER BY category_id, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load menu")
		return
	}
	defer rows.Close()

	items := []models.MenuItem{}
	for rows.Next() {
		var m models.MenuItem
		if err := rows.Scan(&m.ID, &m.CategoryID, &m.Name, &m.Description,
			&m.Price, &m.ImageURL, &m.IsAvailable, &m.StockQuantity, &m.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan menu item")
			return
		}
		items = append(items, m)
	}

	writeJSON(w, http.StatusOK, items)
}

type toggleStockRequest struct {
	IsAvailable   *bool `json:"is_available"`
	StockQuantity *int  `json:"stock_quantity,omitempty"`
}

// PATCH /api/menu/{itemID}/stock — staff-only, flips In Stock / Out of Stock.
func (h *MenuHandler) ToggleStock(w http.ResponseWriter, r *http.Request) {
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid item id")
		return
	}

	var req toggleStockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IsAvailable == nil {
		writeError(w, http.StatusBadRequest, "is_available is required")
		return
	}

	ctx := r.Context()
	tag, err := h.DB.Exec(ctx, `
		UPDATE menu_items
		SET is_available = $1,
		    stock_quantity = COALESCE($2, stock_quantity)
		WHERE id = $3`,
		*req.IsAvailable, req.StockQuantity, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update stock")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "menu item not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": itemID, "is_available": *req.IsAvailable})
}
