package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/service"
)

//go:embed web/*
var web embed.FS

type Handler struct {
	svc *service.Service
}

func New(svc *service.Service) http.Handler {
	h := &Handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/v1/isapi/health", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /isapi/health", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /hk_isapi/health", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	page := func(w http.ResponseWriter, r *http.Request) {
		data, err := web.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "UI missing", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
	}
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /hk_isapi/{$}", page)
	mux.HandleFunc("GET /hk_isapi/index.html", page)
	mux.Handle("/api/", http.HandlerFunc(h.route))
	mux.Handle("/isapi/", http.HandlerFunc(h.route))
	mux.Handle("/hk_isapi/", http.HandlerFunc(h.route))
	return mux
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func errorOut(w http.ResponseWriter, status int, msg string) {
	jsonOut(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		errorOut(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		errorOut(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || (parts[0] != "api" && parts[0] != "isapi" && parts[0] != "hk_isapi") {
		http.NotFound(w, r)
		return
	}
	if parts[0] == "isapi" || parts[0] == "hk_isapi" {
		parts[0] = "api"
	} else if len(parts) >= 3 && parts[1] == "v1" && parts[2] == "isapi" {
		parts = append([]string{"api"}, parts[3:]...)
	}
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "discover":
		h.discover(w, r)
	case "cameras":
		h.cameras(w, r, parts[2:])
	case "schedules":
		h.schedules(w, r, parts[2:])
	case "storage":
		h.storage(w, r, parts[2:])
	case "records":
		if r.Method != "GET" {
			errorOut(w, 405, "method not allowed")
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		jsonOut(w, 200, h.svc.DB.ListRecords(n))
	case "capture":
		h.directCapture(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (h *Handler) cameras(w http.ResponseWriter, r *http.Request, path []string) {
	if len(path) == 0 {
		switch r.Method {
		case "GET":
			jsonOut(w, 200, h.svc.DB.ListCameras())
		case "POST":
			var c model.Camera
			if !decode(w, r, &c) {
				return
			}
			c.ID = ""
			newC, err := h.svc.DB.PutCamera(c)
			if err != nil {
				errorOut(w, 400, err.Error())
				return
			}
			jsonOut(w, 201, newC)
		default:
			errorOut(w, 405, "method not allowed")
		}
		return
	}
	id := path[0]
	if len(path) == 2 && path[1] == "discover" {
		if r.Method != "GET" {
			errorOut(w, 405, "method not allowed")
			return
		}
		c, ok := h.svc.DB.GetCamera(id)
		if !ok {
			errorOut(w, 404, "camera not found")
			return
		}
		kind := c.DeviceType
		if kind == "" {
			kind = "camera"
		}
		result, err := h.svc.Discover(r.Context(), c, kind)
		if err != nil {
			errorOut(w, 502, err.Error())
			return
		}
		jsonOut(w, 200, result)
		return
	}
	if len(path) == 2 && path[1] == "capture" {
		if r.Method != "POST" {
			errorOut(w, 405, "method not allowed")
			return
		}
		if _, ok := h.svc.DB.GetCamera(id); !ok {
			errorOut(w, 404, "camera not found")
			return
		}
		h.respondCapture(w, r, func(ctx context.Context) ([]byte, string, error) {
			return h.svc.CaptureSaved(ctx, id, r.URL.Query().Get("upload") == "true", "manual", "")
		})
		return
	}
	if len(path) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "GET":
		c, ok := h.svc.DB.GetCamera(id)
		if !ok {
			errorOut(w, 404, "camera not found")
			return
		}
		c.HasPassword = c.Password != ""
		c.Password = ""
		jsonOut(w, 200, c)
	case "PUT":
		if _, ok := h.svc.DB.GetCamera(id); !ok {
			errorOut(w, 404, "camera not found")
			return
		}
		var c model.Camera
		if !decode(w, r, &c) {
			return
		}
		c.ID = id
		updated, err := h.svc.DB.PutCamera(c)
		if err != nil {
			errorOut(w, 400, err.Error())
			return
		}
		jsonOut(w, 200, updated)
	case "DELETE":
		if err := h.svc.DB.DeleteCamera(id); err != nil {
			errorOut(w, 404, "camera not found")
			return
		}
		w.WriteHeader(204)
	default:
		errorOut(w, 405, "method not allowed")
	}
}
func (h *Handler) schedules(w http.ResponseWriter, r *http.Request, path []string) {
	if len(path) == 0 {
		switch r.Method {
		case "GET":
			jsonOut(w, 200, h.svc.DB.ListSchedules())
		case "POST":
			var j model.Schedule
			if !decode(w, r, &j) {
				return
			}
			j.ID = ""
			v, err := h.svc.DB.PutSchedule(j)
			if err != nil {
				errorOut(w, 400, err.Error())
				return
			}
			jsonOut(w, 201, v)
		default:
			errorOut(w, 405, "method not allowed")
		}
		return
	}
	id := path[0]
	if len(path) == 2 && path[1] == "run" {
		if r.Method != "POST" {
			errorOut(w, 405, "method not allowed")
			return
		}
		j, ok := h.svc.DB.GetSchedule(id)
		if !ok {
			errorOut(w, 404, "schedule not found")
			return
		}
		if j.Kind == "once" {
			errorOut(w, 400, "one-off schedules are no longer supported")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		camera, _ := h.svc.DB.GetCamera(j.CameraID)
		if len(j.Targets) == 0 && camera.Channels == "" && j.TargetChannels == "" {
			jpeg, key, err := h.svc.CaptureSaved(ctx, j.CameraID, true, "manual-schedule", j.ID)
			if err != nil {
				errorOut(w, 502, err.Error())
				return
			}
			jsonOut(w, 200, map[string]any{"object_key": key, "bytes": len(jpeg), "count": 1})
			return
		}
		count, err := h.svc.RunSchedule(ctx, j, "manual-schedule")
		if err != nil {
			errorOut(w, 502, err.Error())
			return
		}
		jsonOut(w, 200, map[string]any{"count": count})
		return
	}
	if len(path) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PUT":
		if _, ok := h.svc.DB.GetSchedule(id); !ok {
			errorOut(w, 404, "schedule not found")
			return
		}
		var j model.Schedule
		if !decode(w, r, &j) {
			return
		}
		j.ID = id
		v, err := h.svc.DB.PutSchedule(j)
		if err != nil {
			errorOut(w, 400, err.Error())
			return
		}
		jsonOut(w, 200, v)
	case "DELETE":
		if err := h.svc.DB.DeleteSchedule(id); err != nil {
			errorOut(w, 404, "schedule not found")
			return
		}
		w.WriteHeader(204)
	default:
		errorOut(w, 405, "method not allowed")
	}
}
func (h *Handler) storage(w http.ResponseWriter, r *http.Request, path []string) {
	if len(path) > 0 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "GET":
		jsonOut(w, 200, h.svc.DB.PublicStorage())
	case "PUT":
		var s model.Storage
		if !decode(w, r, &s) {
			return
		}
		v, err := h.svc.DB.PutStorage(s)
		if err != nil {
			errorOut(w, 400, err.Error())
			return
		}
		jsonOut(w, 200, v)
	default:
		errorOut(w, 405, "method not allowed")
	}
}
func (h *Handler) directCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errorOut(w, 405, "method not allowed")
		return
	}
	var data struct {
		model.Camera
		Upload bool `json:"upload"`
	}
	if !decode(w, r, &data) {
		return
	}
	data.Camera.ID = ""
	data.Camera.Enabled = true
	if data.Camera.Name == "" {
		data.Camera.Name = "ad-hoc"
	}
	if err := model.ValidateCamera(&data.Camera); err != nil {
		errorOut(w, 400, err.Error())
		return
	}
	if data.Password == "" {
		errorOut(w, 400, "password required")
		return
	}
	h.respondCapture(w, r, func(ctx context.Context) ([]byte, string, error) {
		return h.svc.Capture(ctx, data.Camera, data.Upload, "api", "")
	})
}
func (h *Handler) respondCapture(w http.ResponseWriter, r *http.Request, fn func(context.Context) ([]byte, string, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	jpeg, key, err := fn(ctx)
	if err != nil {
		errorOut(w, 502, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if key != "" {
		w.Header().Set("X-OSS-Object-Key", key)
	}
	w.WriteHeader(200)
	w.Write(jpeg)
}

func (h *Handler) discover(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errorOut(w, 405, "method not allowed")
		return
	}
	var c model.Camera
	if !decode(w, r, &c) {
		return
	}
	if c.ID != "" && c.Password == "" {
		old, ok := h.svc.DB.GetCamera(c.ID)
		if !ok {
			errorOut(w, 404, "camera not found")
			return
		}
		if !strings.EqualFold(strings.TrimRight(c.BaseURL, "/"), strings.TrimRight(old.BaseURL, "/")) || c.Username != old.Username {
			errorOut(w, 400, "password required when changing device address or username")
			return
		}
		c.Password = old.Password
	}
	if c.Name == "" {
		c.Name = "discovery"
	}
	if c.ChannelID == "" {
		c.ChannelID = "101"
	}
	if c.DeviceType == "" {
		errorOut(w, 400, "device_type required")
		return
	}
	result, err := h.svc.Discover(r.Context(), c, c.DeviceType)
	if err != nil {
		errorOut(w, 502, err.Error())
		return
	}
	jsonOut(w, 200, result)
}
