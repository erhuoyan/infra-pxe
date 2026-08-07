package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyops/infra-pxe/internal/config"
	"github.com/joyops/infra-pxe/internal/store"
)

// --- Template CRUD (kickstart / cloud-init) — file-based ---

// POST /api/templates — create or update a template (writes to disk)
func templateUpsertHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			jsonError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if req.Name == "" || req.Content == "" {
			jsonError(w, 400, "name and content are required")
			return
		}
		// Prevent path traversal
		if strings.Contains(req.Name, "..") {
			jsonError(w, 400, "invalid template name")
			return
		}
		p := filepath.Join(cfg.TemplatesDir(), req.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(req.Content), 0o644); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]string{"name": req.Name, "status": "saved"})
	}
}

// GET /api/templates — list all templates
func templateListHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var names []string
		walkTemplates(cfg.TemplatesDir(), "", &names)
		type item struct {
			Name string `json:"name"`
		}
		items := make([]item, 0, len(names))
		for _, n := range names {
			items = append(items, item{Name: n})
		}
		jsonOK(w, items)
	}
}

func walkTemplates(base, prefix string, names *[]string) {
	dir := base
	if prefix != "" {
		dir = filepath.Join(base, prefix)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		rel := name
		if prefix != "" {
			rel = prefix + "/" + name
		}
		if e.IsDir() {
			walkTemplates(base, rel, names)
		} else {
			*names = append(*names, rel)
		}
	}
}

// GET /api/templates/{name...} — get template content
func templateGetHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		content, ok := s.GetTemplate(name)
		if !ok {
			jsonError(w, 404, "template not found: "+name)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(content))
	}
}

// DELETE /api/templates/{name...} — delete a template
func templateDeleteHandler(cfg *config.Config, s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if strings.Contains(name, "..") {
			jsonError(w, 400, "invalid template name")
			return
		}
		p := filepath.Join(cfg.TemplatesDir(), name)
		if err := os.Remove(p); err != nil {
			jsonError(w, 404, "template not found: "+name)
			return
		}
		jsonOK(w, map[string]string{"deleted": name})
	}
}
