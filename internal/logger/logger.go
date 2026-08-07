package logger

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
)

var L *slog.Logger

// Init sets up the global structured logger.
// Writes to both stderr and logFile (if dir is provided).
func Init(logDir string) {
	var handlers []slog.Handler

	// Stderr: human-readable
	handlers = append(handlers, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// File: JSON for machine parsing
	if logDir != "" {
		os.MkdirAll(logDir, 0o755)
		f, err := os.OpenFile(filepath.Join(logDir, "pxe.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			handlers = append(handlers, slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
		}
	}

	var h slog.Handler
	if len(handlers) == 1 {
		h = handlers[0]
	} else {
		h = &multiHandler{handlers: handlers}
	}
	L = slog.New(h)
	slog.SetDefault(L)
}

// multiHandler fans out to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if err := h.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: hs}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: hs}
}
