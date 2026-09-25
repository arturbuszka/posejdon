// Posejdon: serwer PWA do sterowania płytkami w domu (na razie fontanną).
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"posejdon/internal/devices"
	"posejdon/internal/server"
	"posejdon/web"
)

func main() {
	addr := envOr("ADDR", ":8080")
	devicesFile := envOr("DEVICES_FILE", "devices.json")

	devs, err := devices.Load(devicesFile)
	if err != nil {
		slog.Error("cannot load devices", "err", err)
		os.Exit(1)
	}
	for _, d := range devs {
		slog.Info("device", "id", d.ID, "type", d.Type, "url", d.URL)
	}

	handler, err := server.New(devs, web.Files)
	if err != nil {
		slog.Error("cannot build server", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      server.UpstreamTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
