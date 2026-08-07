package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
)

// fileListHandler returns all files currently stored on this node.
func fileListHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filesDir := filepath.Join(cfg.BootDir(), "http")
		var result []map[string]any

		entries, err := os.ReadDir(filesDir)
		if err != nil {
			jsonOK(w, result)
			return
		}

		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "fil-") {
				continue
			}
			bid := e.Name()
			subs, _ := os.ReadDir(filepath.Join(filesDir, bid))
			for _, f := range subs {
				if !f.IsDir() {
					info, _ := f.Info()
					size := int64(0)
					if info != nil {
						size = info.Size()
					}
					result = append(result, map[string]any{
						"bid":      bid,
						"filename": f.Name(),
						"size":     size,
					})
				}
			}
		}
		jsonOK(w, result)
	}
}

// fileUploadHandler receives a multipart file POST
// and writes it to boot/http/{bid}/{filename}.
//
// Request: multipart/form-data with fields:
//   - bid:   string (e.g. "fil-a3f7k2m9")
//   - file:  file content
func fileUploadHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Limit upload size to 2GB
		r.Body = http.MaxBytesReader(w, r.Body, 2<<30)

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			jsonError(w, 400, fmt.Sprintf("Failed to parse multipart form: %v", err))
			return
		}

		bid := r.FormValue("bid")
		if bid == "" {
			jsonError(w, 400, "bid is required")
			return
		}
		if !strings.HasPrefix(bid, "fil-") {
			jsonError(w, 400, "bid must start with fil-")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			jsonError(w, 400, fmt.Sprintf("File not found in request: %v", err))
			return
		}
		defer file.Close()

		targetDir := filepath.Join(cfg.BootDir(), "http", bid)
		os.MkdirAll(targetDir, 0755)
		targetPath := filepath.Join(targetDir, header.Filename)

		dst, err := os.Create(targetPath)
		if err != nil {
			jsonError(w, 500, fmt.Sprintf("Failed to create file: %v", err))
			return
		}
		defer dst.Close()

		written, err := io.Copy(dst, file)
		if err != nil {
			jsonError(w, 500, fmt.Sprintf("Failed to write file: %v", err))
			os.Remove(targetPath)
			return
		}

		jsonOK(w, map[string]any{
			"bid":      bid,
			"filename": header.Filename,
			"size":     written,
			"path":     fmt.Sprintf("/files/%s/%s", bid, header.Filename),
		})
	}
}

// fileDeleteHandler removes a bid directory from boot/http/.
func fileDeleteHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bid := r.PathValue("bid")
		if bid == "" || !strings.HasPrefix(bid, "fil-") {
			jsonError(w, 400, "invalid bid")
			return
		}

		targetDir := filepath.Join(cfg.BootDir(), "http", bid)
		if _, err := os.Stat(targetDir); os.IsNotExist(err) {
			jsonOK(w, map[string]string{"status": "not_found", "bid": bid})
			return
		}

		if err := os.RemoveAll(targetDir); err != nil {
			jsonError(w, 500, fmt.Sprintf("Failed to remove: %v", err))
			return
		}

		jsonOK(w, map[string]string{"status": "deleted", "bid": bid})
	}
}
