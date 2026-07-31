package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s, err := store.Open(os.Getenv("PM_DATA_FILE"))
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	address := strings.TrimSpace(os.Getenv("PM_HTTP_ADDR"))
	if address == "" {
		address = ":8082"
	}
	requireIdentity := strings.EqualFold(os.Getenv("PM_REQUIRE_IDENTITY_HEADERS"), "true")
	server := &http.Server{Addr: address, Handler: httpapi.New(s, logger, requireIdentity), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	logger.Info("project management api started", "address", address, "identity_headers_required", requireIdentity)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
