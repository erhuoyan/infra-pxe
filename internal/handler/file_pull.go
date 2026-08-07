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

// fileDownloadState tracks active file downloads (pull from URL)
type fileDownloadState struct {
	mu        sync.RWMutex
	downloads map[string]*fileDownloadProgress // bid → progress
}

type fileDownloadProgress struct {
	Status      string `json:"status"`   // downloading / ready / failed
	Progress    int    `json:"progress"` // 0-100
	TotalBytes  int64  `json:"total_bytes"`
	Downloaded  int64  `json:"downloaded"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

var fileDownloads = &fileDownloadState{
	downloads: make(map[string]*fileDownloadProgress),
}

func (s *fileDownloadState) get(bid string) *fileDownloadProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloads[bid]
}

func (s *fileDownloadState) set(bid string, p *fileDownloadProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloads[bid] = p
}

// fileCheckHandler checks if a file exists on this Worker.
// GET /api/files/{bid}/check → {exists, filename, size}
func fileCheckHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bid := r.PathValue("bid")
		if bid == "" || !strings.HasPrefix(bid, "fil-") {
			jsonError(w, 400, "invalid bid")
			return
		}

		dir := filepath.Join(cfg.BootDir(), "http", bid)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			// Check if downloading
			if p := fileDownloads.get(bid); p != nil {
				jsonOK(w, map[string]any{
					"exists":   false,
					"status":   p.Status,
					"progress": p.Progress,
				})
				return
			}
			jsonOK(w, map[string]any{"exists": false})
			return
		}

		// Find first file in the bid directory
		for _, e := range entries {
			if !e.IsDir() {
				info, _ := e.Info()
				size := int64(0)
				if info != nil {
					size = info.Size()
				}
				jsonOK(w, map[string]any{
					"exists":   true,
					"filename": e.Name(),
					"size":     size,
				})
				return
			}
		}
		jsonOK(w, map[string]any{"exists": false})
	}
}

// filePullHandler triggers a background file download from a URL.
// POST /api/files/pull  body: {"bid": "fil-xxx", "filename": "NVIDIA.run", "url": "http://..."}
func filePullHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BID      string `json:"bid"`
			Filename string `json:"filename"`
			URL      string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, 400, "invalid body")
			return
		}
		if body.BID == "" || !strings.HasPrefix(body.BID, "fil-") {
			jsonError(w, 400, "bid required and must start with fil-")
			return
		}
		if body.URL == "" {
			jsonError(w, 400, "url required")
			return
		}
		if body.Filename == "" {
			// Derive from URL
			parts := strings.Split(body.URL, "/")
			body.Filename = parts[len(parts)-1]
		}
		if body.Filename == "" {
			jsonError(w, 400, "cannot determine filename")
			return
		}

		targetDir := filepath.Join(cfg.BootDir(), "http", body.BID)
		destPath := filepath.Join(targetDir, body.Filename)

		// Check if already downloading
		if p := fileDownloads.get(body.BID); p != nil && p.Status == "downloading" {
			jsonOK(w, map[string]any{
				"status":   "already_downloading",
				"progress": p.Progress,
			})
			return
		}

		// Check if file already exists
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			jsonOK(w, map[string]any{
				"status":   "already_exists",
				"bid":      body.BID,
				"filename": body.Filename,
				"size":     info.Size(),
			})
			return
		}

		// Start background download
		os.MkdirAll(targetDir, 0755)
		progress := &fileDownloadProgress{
			Status:    "downloading",
			StartedAt: time.Now().Format(time.RFC3339),
		}
		fileDownloads.set(body.BID, progress)

		go doFileDownload(body.URL, destPath, body.BID, body.Filename, progress)

		jsonOK(w, map[string]any{
			"status":   "started",
			"bid":      body.BID,
			"filename": body.Filename,
			"url":      body.URL,
		})
	}
}

func doFileDownload(url, destPath, bid, filename string, progress *fileDownloadProgress) {
	slog.Info("file download started", "bid", bid, "filename", filename, "url", url)

	resp, err := http.Get(url)
	if err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("HTTP GET failed: %v", err)
		slog.Error("file download failed", "bid", bid, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		slog.Error("file download failed", "bid", bid, "status", resp.StatusCode)
		return
	}

	progress.TotalBytes = resp.ContentLength

	tmpPath := destPath + ".downloading"
	out, err := os.Create(tmpPath)
	if err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("create file failed: %v", err)
		return
	}

	buf := make([]byte, 256*1024) // 256KB buffer
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				out.Close()
				os.Remove(tmpPath)
				progress.Status = "failed"
				progress.Error = fmt.Sprintf("write failed: %v", wErr)
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
			out.Close()
			os.Remove(tmpPath)
			progress.Status = "failed"
			progress.Error = fmt.Sprintf("read failed: %v", readErr)
			slog.Error("file download read error", "bid", bid, "err", readErr)
			return
		}
	}
	out.Close()

	// Atomic rename
	if err := os.Rename(tmpPath, destPath); err != nil {
		progress.Status = "failed"
		progress.Error = fmt.Sprintf("rename failed: %v", err)
		return
	}

	progress.Status = "ready"
	progress.Progress = 100
	progress.CompletedAt = time.Now().Format(time.RFC3339)
	slog.Info("file download complete", "bid", bid, "filename", filename, "bytes", progress.Downloaded)
}
