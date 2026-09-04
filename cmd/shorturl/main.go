// Command shorturl serves short links from Firestore. It is the Cloud Run
// replacement for the redirV2 Cloud Function.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/scjalliance/shorturl/internal/shorturl"
)

// version is stamped at build time with
// -ldflags "-X main.version=<short git sha>".
var version = "unknown"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: cloudLoggingAttrs}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// run wires the Firestore client and HTTP server and blocks until SIGTERM.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = firestore.DetectProjectID
	}
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		return err
	}
	defer client.Close()

	h := &shorturl.Handler{
		Store:        &shorturl.FirestoreStore{Client: client},
		Version:      version,
		HostOverride: os.Getenv("SHORTURL_HOSTNAME"),
		Logger:       logger,
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("listening", "addr", srv.Addr, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// cloudLoggingAttrs renames slog's level and message keys to the field names
// Cloud Logging parses from stdout JSON.
func cloudLoggingAttrs(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.LevelKey:
		a.Key = "severity"
		if a.Value.Any() == slog.LevelWarn {
			a.Value = slog.StringValue("WARNING")
		}
	case slog.MessageKey:
		a.Key = "message"
	}
	return a
}
