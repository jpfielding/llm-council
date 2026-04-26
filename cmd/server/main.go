package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jpfielding/llm-council/api"
	"github.com/jpfielding/llm-council/config"
	"github.com/jpfielding/llm-council/council"
	"github.com/jpfielding/llm-council/openrouter"
	"github.com/jpfielding/llm-council/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load", "err", err)
		os.Exit(1)
	}

	store, err := storage.NewStore(cfg.DataDir)
	if err != nil {
		slog.Error("storage init", "err", err)
		os.Exit(1)
	}

	orClient := openrouter.NewClient(cfg.OpenRouterAPIKey)
	c := council.New(council.Config{
		Models:     cfg.CouncilModels,
		Chairman:   cfg.Chairman,
		TitleModel: cfg.TitleModel,
	}, orClient)

	h, err := api.New(api.Config{
		Store:     store,
		Council:   c,
		Models:    cfg.CouncilModels,
		Chairman:  cfg.Chairman,
		AuthToken: cfg.AuthToken,
	})
	if err != nil {
		slog.Error("handler init", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      h.Routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // disabled for SSE streaming
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("llm-council listening",
			"addr", srv.Addr,
			"council", cfg.CouncilModels,
			"chairman", cfg.Chairman,
			"data_dir", cfg.DataDir,
			"auth", cfg.AuthToken != "")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
