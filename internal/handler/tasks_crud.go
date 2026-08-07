package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/joyops/infra-pxe/internal/db"
	"github.com/joyops/infra-pxe/internal/dnsmasq"
	"github.com/joyops/infra-pxe/internal/store"
)

// --- Task CRUD ---

// POST /api/tasks — create a single task
func taskCreateHandler(s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req db.TaskCreate
		if err := json.Unmarshal(body, &req); err != nil {
			jsonError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if req.SN == "" {
			jsonError(w, 400, "sn is required")
			return
		}

		// Check dnsmasq is alive (not zombie)
		if !d.IsRunning() {
			jsonError(w, 400, "dnsmasq is not running — target machines cannot PXE boot. Start it with dnsmasq_start or fix the zombie process.")
			return
		}

		// Validate OS template and all its physical resources
		if req.OS != "" {
			v := s.ValidateOSTemplate(req.OS)
			if v.Template.Status == "fail" && v.Template.Detail == "os_template not found" {
				available, _ := s.DB.ListOSTemplates()
				var bids []string
				for _, t := range available {
					bids = append(bids, t.Bid)
				}
				jsonError(w, 400, "os template not found: "+req.OS+". available: "+strings.Join(bids, ", "))
				return
			}
			if !v.Ready {
				// Build error message from failed checks
				var problems []string
				if v.Template.Status == "fail" {
					problems = append(problems, "template: "+v.Template.Detail)
				}
				if v.ISO.Status == "fail" {
					problems = append(problems, "iso: "+v.ISO.Detail+" ("+v.ISO.Expected+")")
				}
				if v.ISOMounted.Status == "fail" {
					problems = append(problems, "mount: "+v.ISOMounted.Detail+" ("+v.ISOMounted.Expected+")")
				}
				jsonError(w, 400, "os template '"+req.OS+"' not ready: "+strings.Join(problems, "; "))
				return
			}
		}

		// Inherit scripts/files from os_template if not explicitly provided
		if req.OS != "" {
			s.InheritTemplateResources(&req)
		}

		task, err := s.DB.CreateTask(&req)
		if err != nil {
			jsonError(w, 409, "create task: "+err.Error())
			return
		}
		d.RegenerateConfig()
		jsonOK(w, task)
	}
}

// GET /api/tasks — list tasks (optional ?status=pending)
func taskListHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		tasks, err := s.DB.ListTasks(status)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		if tasks == nil {
			tasks = []db.Task{}
		}
		jsonOK(w, tasks)
	}
}

// GET /api/tasks/{sn} — get single task
func taskGetHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		task, err := s.DB.GetTaskBySN(sn)
		if err != nil {
			jsonError(w, 404, "task not found: "+sn)
			return
		}
		jsonOK(w, task)
	}
}

// PUT /api/tasks/{sn} — update task fields
func taskUpdateHandler(s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		body, _ := io.ReadAll(r.Body)
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}
		if err := s.DB.UpdateTask(sn, fields); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		d.RegenerateConfig()
		task, _ := s.DB.GetTaskBySN(sn)
		jsonOK(w, task)
	}
}

// DELETE /api/tasks/{sn} — delete task
func taskDeleteHandler(s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sn := r.PathValue("sn")
		if err := s.DB.DeleteTask(sn); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		d.RegenerateConfig()
		jsonOK(w, map[string]string{"deleted": sn})
	}
}

// POST /api/tasks/batch — batch create tasks
func taskBatchCreateHandler(s *store.Store, d *dnsmasq.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var tasks []db.TaskCreate
		if err := json.Unmarshal(body, &tasks); err != nil {
			jsonError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		count, err := s.DB.BatchCreateTasks(tasks)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		d.RegenerateConfig()
		jsonOK(w, map[string]any{"created": count})
	}
}
