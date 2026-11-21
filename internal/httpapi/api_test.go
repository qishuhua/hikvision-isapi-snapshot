package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/service"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/store"
)

func TestCaptureAPIAndSecurity(t *testing.T) {
	cameraHits := 0
	camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cameraHits++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="nvr",nonce="123",qop="auth"`)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/ISAPI/ContentMgmt/StreamingProxy/channels/201/picture" {
			t.Errorf("camera path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	}))
	defer camera.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	db, err := store.New(filepath.Join(t.TempDir(), "state.json"), key)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(New(service.New(db, 2)))
	defer api.Close()
	noAuth, _ := http.Get(api.URL + "/api/cameras")
	if noAuth.StatusCode != 200 {
		t.Errorf("anonymous status %d", noAuth.StatusCode)
	}
	noAuth.Body.Close()
	do := func(method, path string, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, api.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("root", "pw")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	inline := map[string]any{"base_url": camera.URL, "username": "admin", "password": "pass", "channel_id": "201", "endpoint_mode": "proxy", "upload": false}
	payload, _ := json.Marshal(inline)
	resp := do("POST", "/api/capture", payload)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("capture status=%d %s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(b) != 4 || cameraHits != 2 {
		t.Fatalf("incorrect capture %v hits=%d", b, cameraHits)
	}
	cameraConfig := map[string]any{"name": "Camera-2", "base_url": camera.URL, "username": "admin", "password": "save-me", "channel_id": "201", "endpoint_mode": "proxy", "enabled": true}
	payload, _ = json.Marshal(cameraConfig)
	resp = do("POST", "/api/cameras", payload)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("save camera %d %s", resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "save-me") {
		t.Fatal("API returned secret")
	}
	var saved map[string]any
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	id, _ := saved["id"].(string)
	if id == "" {
		t.Fatal("missing id")
	}
	resp = do("POST", "/api/cameras/"+id+"/capture", nil)
	if resp.StatusCode != 200 {
		t.Errorf("saved capture status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do("GET", "/api/records", nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte(`"status":"success"`)) {
		t.Errorf("missing successful record: %s", body)
	}
}

func TestVersionedRoutesAndLegacyCompatibility(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	db, err := store.New(filepath.Join(t.TempDir(), "state.json"), key)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service.New(db, 2))
	for _, prefix := range []string{"/api", "/api/v1/isapi", "/isapi", "/hk_isapi"} {
		for _, tc := range []struct {
			path string
			auth bool
			want int
		}{
			{"/health", false, 200}, {"/cameras", false, 200}, {"/cameras", true, 200},
			{"/schedules", true, 200}, {"/storage", true, 200}, {"/records", true, 200},
			{"/missing", true, 404},
		} {
			req := httptest.NewRequest(http.MethodGet, prefix+tc.path, nil)
			if tc.auth {
				req.SetBasicAuth("root", "pw")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s%s: got %d want %d", prefix, tc.path, rec.Code, tc.want)
			}
		}
	}
}
func TestDiscoverUsesSavedCredentialsOnlyForSameDevice(t *testing.T) {
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ISAPI/System/deviceInfo" {
			io.WriteString(w, `<DeviceInfo><deviceName>camera</deviceName></DeviceInfo>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer device.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	db, err := store.New(filepath.Join(t.TempDir(), "state.json"), key)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.PutCamera(model.Camera{Name: "camera", BaseURL: device.URL, Username: "admin", Password: "secret", ChannelID: "101", DeviceType: "camera", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service.New(db, 2))
	for _, tc := range []struct {
		url  string
		want int
	}{{device.URL, 200}, {"http://example.invalid", 400}} {
		body, _ := json.Marshal(map[string]any{"id": saved.ID, "base_url": tc.url, "username": "admin", "device_type": "camera", "channel_id": "101"})
		req := httptest.NewRequest("POST", "/hk_isapi/discover", bytes.NewReader(body))
		req.SetBasicAuth("root", "pw")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("discovery returned %d want %d: %s", rec.Code, tc.want, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Fatal("password exposed")
		}
	}
}

func TestManagementPagePaths(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	db, err := store.New(filepath.Join(t.TempDir(), "state.json"), key)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service.New(db, 2))
	for _, path := range []string{"/", "/hk_isapi/", "/hk_isapi/index.html"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "ISAPI Snapshot") {
			t.Errorf("page %s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
