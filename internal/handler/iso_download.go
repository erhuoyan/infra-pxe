package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joyops/infra-pxe/internal/config"
)

// isoDownloadState tracks active downloads
type isoDownloadState struct {
	mu        sync.RWMutex
	downloads map[string]*downloadProgress // filename → progress
}

type downloadProgress struct {
	Status      string `json:"status"`       // downloading / ready / failed
	Progress    int    `json:"progress"`     // 0-100
	TotalBytes  int64  `json:"total_bytes"`
	Downloaded  int64  `json:"downloaded"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

var isoDownloads = &isoDownloadState{
	downloads: make(map[string]*downloadProgress),
}

func (s *isoDownloadState) get(filename string) *downloadProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloads[filename]
}

func (s *isoDownloadState) set(filename string, p *downloadProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloads[filename] = p
}

func (s *isoDownloadState) remove(filename string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.downloads, filename)
}

// isoDownloadHandler triggers a background ISO download.
// POST /api/iso/download  body: {"url": "http://...", "filename": "xxx.iso"}
func isoDownloadHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL      string `json:"url"`
			Filename string `json:"filename"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, 400, "invalid body")
			return
		}
		if body.URL == "" {
			jsonError(w, 400, "url required")
			return
		}
		if body.Filename == "" {
			// Derive filename from URL
			parts := strings.Split(body.URL, "/")
			body.Filename = parts[len(parts)-1]
		}
		if body.Filename == "" {
			jsonError(w, 400, "cannot determine filename")
			return
		}

		isoDir := filepath.Join(cfg.BootDir(), "iso")
		destPath := filepath.Join(isoDir, body.Filename)

		// Check if already downloading
		if p := isoDownloads.get(body.Filename); p != nil && p.Status == "downloading" {
			jsonOK(w, map[string]any{
				"status":  "already_downloading",
				"progress": p.Progress,
			})
			return
		}

		// Check if file already exists and is complete
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			jsonOK(w, map[string]any{
				"status":   "already_exists",
				"size_mb":  info.Size() / (1024 * 1024),
				"filename": body.Filename,
			})
			return
		}

		// Start background download
		os.MkdirAll(isoDir, 0755)
		progress := &downloadProgress{
			Status:    "downloading",
			StartedAt: time.Now().Format(time.RFC3339),
		}
		isoDownloads.set(body.Filename, progress)

		go doISODownload(body.URL, destPath, body.Filename, progress)

		jsonOK(w, map[string]any{
			"status":   "started",
			"filename": body.Filename,
			"url":      body.URL,
		})
	}
}

func doISODownload(url, destPath, filename string, progress *downloadProgress) {
	slog.Info("ISO download started", "filename", filename, "url", url)

	resp, err := http.Get(url)
	if err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("HTTP GET failed: %v", err)
		slog.Error("ISO download failed", "filename", filename, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		slog.Error("ISO download failed", "filename", filename, "status", resp.StatusCode)
		return
	}

	progress.TotalBytes = resp.ContentLength

	// Write to temp file first, then rename
	tmpPath := destPath + ".downloading"
	f, err := os.Create(tmpPath)
	if err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("create file: %v", err)
		return
	}

	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				os.Remove(tmpPath)
				progress.Status = "failed"
				progress.Error = fmt.Sprintf("write: %v", writeErr)
				return
			}
			progress.Downloaded += int64(n)
			if progress.TotalBytes > 0 {
				progress.Progress = int(progress.Downloaded * 100 / progress.TotalBytes)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(tmpPath)
			progress.Status = "failed"
			progress.Error = fmt.Sprintf("read: %v", readErr)
			slog.Error("ISO download read error", "filename", filename, "err", readErr)
			return
		}
	}
	f.Close()

	// Rename temp → final
	if err := os.Rename(tmpPath, destPath); err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("rename: %v", err)
		return
	}

	progress.Status = "ready"
	progress.Progress = 100
	progress.CompletedAt = time.Now().Format(time.RFC3339)
	slog.Info("ISO download complete", "filename", filename, "mb", progress.Downloaded/(1024*1024))
}

// getISOStatus returns download status for a filename (for isoListHandler enrichment)
func getISOStatus(filename string) (status string, progress int) {
	if p := isoDownloads.get(filename); p != nil {
		return p.Status, p.Progress
	}
	return "", 0
}
