package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
}

// Load reads .env (if present) and environment variables into a Config.
func Load() *Config {
	_ = godotenv.Load() // ignore error: .env is optional (e.g. in prod, real env vars are used)

	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/aso_kitchen?sslmode=disable"),
		JWTSecret:   getEnv("JWT_SECRET", "dev-secret-change-me"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
