package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/asokitchen/backend/internal/config"
	"github.com/asokitchen/backend/internal/database"
	"github.com/asokitchen/backend/internal/handlers"
	authmw "github.com/asokitchen/backend/internal/middleware"
	"github.com/asokitchen/backend/internal/models"
	"github.com/asokitchen/backend/internal/websocket"
)

func main() {
	cfg := config.Load()

	pool, err := database.NewPool(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()
	log.Println("connected to postgres")

	hub := websocket.NewHub()

	menuHandler := handlers.NewMenuHandler(pool)
	orderHandler := handlers.NewOrderHandler(pool, hub)
	authHandler := handlers.NewAuthHandler(pool, cfg.JWTSecret)

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(15 * time.Second))
	r.Use(cors.Handler(cors.Options{
		// Restrict this to your actual web + mobile app origins in production.
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Route("/api", func(r chi.Router) {
		// ---------- Public routes: no token required ----------
		r.Post("/signup", authHandler.Signup)
		r.Post("/login", authHandler.Login)
		r.Get("/menu", menuHandler.ListMenu)

		// ---------- Protected routes: valid JWT required ----------
		r.Group(func(r chi.Router) {
			r.Use(authmw.RequireAuth(cfg.JWTSecret))

			// Any authenticated user (customer, staff, or admin) can place
			// and view their own orders.
			r.Post("/orders", orderHandler.CreateOrder)
			r.Get("/orders/{orderID}", orderHandler.GetOrder)

			// Staff/admin only: updating order status and toggling stock
			// are kitchen-side operations, not customer actions.
			r.Group(func(r chi.Router) {
				r.Use(authmw.RequireRole(string(models.RoleStaff), string(models.RoleAdmin)))
				r.Patch("/orders/{orderID}/status", orderHandler.UpdateOrderStatus)
				r.Patch("/menu/{itemID}/stock", menuHandler.ToggleStock)
			})
		})
	})

	// Live order tracking
	r.Get("/ws/orders/{orderID}", orderHandler.SubscribeOrder)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("aso kitchen backend listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("server stopped cleanly")
}
