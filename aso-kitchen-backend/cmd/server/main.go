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
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/asokitchen/backend/internal/config"
	"github.com/asokitchen/backend/internal/database"
	"github.com/asokitchen/backend/internal/handlers"
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

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))
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
		// Menu
		r.Get("/menu", menuHandler.ListMenu)
		r.Patch("/menu/{itemID}/stock", menuHandler.ToggleStock)

		// Orders
		r.Post("/orders", orderHandler.CreateOrder)
		r.Get("/orders/{orderID}", orderHandler.GetOrder)
		r.Patch("/orders/{orderID}/status", orderHandler.UpdateOrderStatus)
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
