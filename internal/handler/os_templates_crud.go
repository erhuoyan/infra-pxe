package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/store"
)

// --- OS Template CRUD ---

// POST /api/os-templates — create or update
func osTemplateUpsertHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var t db.OSTemplate
		if err := json.Unmarshal(body, &t); err != nil {
			jsonError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if t.Bid == "" {
			jsonError(w, 400, "bid is required")
			return
		}
		if err := s.DB.UpsertOSTemplate(&t); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, t)
	}
}

// GET /api/os-templates — list all
func osTemplateListHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tpls, err := s.DB.ListOSTemplates()
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		if tpls == nil {
			tpls = []db.OSTemplate{}
		}
		jsonOK(w, tpls)
	}
}

// GET /api/os-templates/{bid} — get single
func osTemplateGetHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bid := r.PathValue("bid")
		t, err := s.DB.GetOSTemplate(bid)
		if err != nil {
			jsonError(w, 404, "os template not found: "+bid)
			return
		}
		jsonOK(w, t)
	}
}

// DELETE /api/os-templates/{bid} — delete
func osTemplateDeleteHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bid := r.PathValue("bid")
		if err := s.DB.DeleteOSTemplate(bid); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]string{"deleted": bid})
	}
}
