package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
)

// ISO management — scan iso dir, mount/umount, list mounted

func isoListHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bootDir := cfg.BootDir()
		isoDir := filepath.Join(bootDir, "iso")
		httpDir := filepath.Join(bootDir, "http")

		var isos []map[string]any
		filepath.Walk(isoDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".iso") {
				return nil
			}
			rel, _ := filepath.Rel(isoDir, path)

			// Check mounted status: scan httpDir for any repo/ that uses this ISO
			mounted := false
			mountedPath := ""
			if data, err := os.ReadFile("/proc/mounts"); err == nil {
				absIso, _ := filepath.EvalSymlinks(filepath.Join(isoDir, rel))
				for _, line := range strings.Split(string(data), "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 2 && fields[0] == absIso {
						mounted = true
						mountedPath = fields[1]
						break
					}
				}
			}

			// Derive distro_path from mount point or filename
			distroPath := ""
			if mountedPath != "" {
				// e.g. /joyops/infra/pxe/boot/http/centos/7.9/x86_64/repo → centos/7.9/x86_64
				if rel, err := filepath.Rel(httpDir, mountedPath); err == nil {
					distroPath = strings.TrimSuffix(rel, "/repo")
				}
			}

			// Download status
			dlStatus, dlProgress := getISOStatus(info.Name())
			status := "ready"
			if dlStatus == "downloading" {
				status = "downloading"
			}

			// Use os.Stat to follow symlinks and get real file size
			fileSize := info.Size()
			if realInfo, err := os.Stat(path); err == nil {
				fileSize = realInfo.Size()
			}
			isos = append(isos, map[string]any{
				"filename":    info.Name(),
				"path":        rel,
				"size_mb":     fileSize / (1024 * 1024),
				"distro_path": distroPath,
				"mounted":     mounted,
				"status":      status,
				"progress":    dlProgress,
			})
			return nil
		})

		if isos == nil {
			isos = []map[string]any{}
		}
		jsonOK(w, isos)
	}
}

func isoMountHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Filename   string `json:"filename"`    // ISO filename (relative to boot/iso/)
			DistroPath string `json:"distro_path"` // e.g. "centos/7.9/x86_64" — mount to boot/http/{distro_path}/repo
		}
		json.Unmarshal(body, &req)

		if req.Filename == "" {
			jsonError(w, 400, "filename is required")
			return
		}

		bootDir := cfg.BootDir()
		isoDir := filepath.Join(bootDir, "iso")
		httpDir := filepath.Join(bootDir, "http")
		isoPath := filepath.Join(isoDir, req.Filename)

		// Verify ISO exists
		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			jsonError(w, 404, fmt.Sprintf("ISO not found: %s", req.Filename))
			return
		}

		// Determine mount path
		distroPath := req.DistroPath
		if distroPath == "" {
			// Fallback: use filename without extension
			distroPath = strings.TrimSuffix(filepath.Base(req.Filename), filepath.Ext(req.Filename))
		}
		repoPath := filepath.Join(httpDir, distroPath, "repo")

		// Check if already mounted
		if isMountPoint(repoPath) {
			jsonOK(w, map[string]string{"status": "already_mounted", "path": repoPath})
			return
		}

		// Create mount point and mount
		os.MkdirAll(repoPath, 0o755)
		cmd := exec.Command("mount", "-o", "loop,ro", isoPath, repoPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			jsonError(w, 500, fmt.Sprintf("mount failed: %s", strings.TrimSpace(string(output))))
			return
		}

		jsonOK(w, map[string]any{
			"status":      "mounted",
			"iso":         req.Filename,
			"distro_path": distroPath,
			"repo_path":   repoPath,
			"repo_url":    fmt.Sprintf("/files/%s/repo", distroPath),
		})
	}
}

func isoUmountHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			DistroPath string `json:"distro_path"` // e.g. "centos/7.9/x86_64"
		}
		json.Unmarshal(body, &req)

		if req.DistroPath == "" {
			jsonError(w, 400, "distro_path is required")
			return
		}

		bootDir := cfg.BootDir()
		httpDir := filepath.Join(bootDir, "http")
		repoPath := filepath.Join(httpDir, req.DistroPath, "repo")

		if !isMountPoint(repoPath) {
			jsonOK(w, map[string]string{"status": "not_mounted"})
			return
		}

		cmd := exec.Command("umount", repoPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			jsonError(w, 500, fmt.Sprintf("umount failed: %s", strings.TrimSpace(string(output))))
			return
		}

		jsonOK(w, map[string]string{"status": "unmounted", "distro_path": req.DistroPath})
	}
}

func isoMountedHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bootDir := cfg.BootDir()
		httpDir := filepath.Join(bootDir, "http")

		// Walk all subdirs looking for mount points at */repo
		var mounted []map[string]any
		filepath.Walk(httpDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() {
				return nil
			}
			if info.Name() == "repo" {
				if isMountPoint(path) {
					parent, _ := filepath.Rel(httpDir, filepath.Dir(path))
					mounted = append(mounted, map[string]any{
						"distro_path": parent,
						"repo_path":   path,
						"repo_url":    fmt.Sprintf("/files/%s/repo", parent),
					})
				}
				return filepath.SkipDir // Don't walk inside mounted repo
			}
			return nil
		})
		if mounted == nil {
			mounted = []map[string]any{}
		}
		jsonOK(w, mounted)
	}
}

// isMountPoint checks if a path is a mount point using /proc/mounts
func isMountPoint(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == absPath {
			return true
		}
	}
	return false
}
