package handler

import (
	"net/http"

	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/store"
)

// --- Results API ---

// GET /api/results — list install results (history)
// Query params: ?sn=xxx&limit=100
func resultsListHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.URL.Query().Get("sn")
		results, err := s.DB.ListResults(sn, 100)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		if results == nil {
			results = []db.Result{}
		}
		jsonOK(w, results)
	}
}

// GET /api/results/{sn} — results for a specific SN
func resultsBySNHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		results, err := s.DB.GetResultsBySN(sn)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		if results == nil {
			results = []db.Result{}
		}
		jsonOK(w, results)
	}
}
