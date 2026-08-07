// infra-pxe: self-contained PXE Engine.
// Single static binary with SQLite — complete install lifecycle via REST API.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
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

func main() {
	cfgPath := flag.String("config", "conf/pxe.yaml", "path to PXE config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Initialize structured logger (stderr + file)
	logger.Init(cfg.LogDir())

	// Ensure data directories
	store.EnsureDirs(cfg)

	// Initialize SQLite-backed store
	taskStore := store.New(cfg)

	// dnsmasq manager
	dnsmasqMgr := dnsmasq.New(cfg, taskStore)

	// Auto-start dnsmasq if DHCP config exists in DB
	if dbCfg := taskStore.DB.GetDhcpConfig(); dbCfg.Interface != "" && dbCfg.DhcpStart != "" {
		if err := dnsmasqMgr.Start(); err != nil {
			slog.Warn("dnsmasq auto-start failed (will retry on API call)", "err", err)
		} else {
			slog.Info("dnsmasq auto-started from DB config")
		}
	}

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx // used by shutdown goroutine

	// HTTP handler (CRUD API + PXE rendering + static files)
	mux := handler.New(cfg, taskStore, dnsmasqMgr, cancel)

	addr := net.JoinHostPort(cfg.ListenAddr(), strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down")
		cancel()
		dnsmasqMgr.Stop()
		slog.Info("dnsmasq stopped")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		taskStore.Close()
	}()

	slog.Info("PXE Engine listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
