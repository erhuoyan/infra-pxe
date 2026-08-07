package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/dnsmasq"
	"github.com/joyops/infra-pxe/internal/handler"
	"github.com/joyops/infra-pxe/internal/logger"
	"github.com/joyops/infra-pxe/internal/store"
)

// Run starts the PXE Engine HTTP server and blocks until shutdown.
func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger.Init(cfg.LogDir())
	store.EnsureDirs(cfg)

	taskStore := store.New(cfg)
	defer taskStore.Close()

	dnsmasqMgr := dnsmasq.New(cfg, taskStore)
	defer func() {
		dnsmasqMgr.Stop()
		slog.Info("dnsmasq stopped")
	}()

	if dbCfg := taskStore.DB.GetDhcpConfig(); dbCfg.Interface != "" && dbCfg.DhcpStart != "" {
		if err := dnsmasqMgr.Start(); err != nil {
			slog.Warn("dnsmasq auto-start failed (will retry on API call)", "err", err)
		} else {
			slog.Info("dnsmasq auto-started from DB config")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	mux := handler.New(cfg, taskStore, dnsmasqMgr, cancel)
	addr := net.JoinHostPort(cfg.ListenAddr(), strconv.Itoa(cfg.Engine.Port))
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("PXE Engine listening", "addr", addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveErr
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
