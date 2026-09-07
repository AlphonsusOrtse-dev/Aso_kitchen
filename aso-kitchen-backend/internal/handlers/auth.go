package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/asokitchen/backend/internal/models"
)

type AuthHandler struct {
	DB        *pgxpool.Pool
	JWTSecret string
}

func NewAuthHandler(db *pgxpool.Pool, jwtSecret string) *AuthHandler {
	return &AuthHandler{DB: db, JWTSecret: jwtSecret}
}

// ---------- Signup ----------

type signupRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Phone    string `json:"phone,omitempty"`
	Password string `json:"password"`
	// NOTE: deliberately no "role" field here. Public signup can only ever
	// create customers — staff/admin accounts are created separately
	// (for now: inserted directly in the database). Never trust a
	// client-supplied role at signup time.
}

// POST /api/signup — public. Always creates a `customer` account.
func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FullName == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "full_name, email and password are required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	var phone *string
	if req.Phone != "" {
		phone = &req.Phone
	}

	userID := uuid.New()
	ctx := r.Context()
	_, err = h.DB.Exec(ctx, `
		INSERT INTO users (id, full_name, email, phone, password_hash, role)
		VALUES ($1, $2, $3, $4, $5, 'customer')`,
		userID, req.FullName, req.Email, phone, string(hash))
	if err != nil {
		// Likely a duplicate email/phone (UNIQUE constraint) — don't leak
		// which field collided, just report a generic conflict.
		writeError(w, http.StatusConflict, "could not create account (email may already be in use)")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":    userID,
		"email": req.Email,
		"role":  models.RoleCustomer,
	})
}

// ---------- Login ----------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
	User  struct {
		ID       uuid.UUID `json:"id"`
		FullName string    `json:"full_name"`
		Role     string    `json:"role"`
	} `json:"user"`
}

// POST /api/login — public. Verifies credentials, returns a signed JWT.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	var (
		userID       uuid.UUID
		fullName     string
		passwordHash string
		role         string
	)
	err := h.DB.QueryRow(ctx,
		`SELECT id, full_name, password_hash, role FROM users WHERE email = $1`,
		req.Email,
	).Scan(&userID, &fullName, &passwordHash, &role)

	// Deliberately identical error for "no such user" and "wrong password" —
	// don't reveal to an attacker which part was wrong.
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := generateJWT(userID, role, h.JWTSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	var resp loginResponse
	resp.Token = token
	resp.User.ID = userID
	resp.User.FullName = fullName
	resp.User.Role = role

	writeJSON(w, http.StatusOK, resp)
}

// generateJWT creates a signed token carrying the user's ID and role.
// This is the "stamped ticket" — anyone holding the JWTSecret could forge
// one of these, which is why that secret must never leak.
func generateJWT(userID uuid.UUID, role string, secret string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID.String(), // "subject" — standard claim name for the user id
		"role": role,
		"exp":  time.Now().Add(24 * time.Hour).Unix(), // token expires in 24h
		"iat":  time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
