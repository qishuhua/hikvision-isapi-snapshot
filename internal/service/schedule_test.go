package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/store"
)

func TestScheduleContinuesAfterDeviceFailure(t *testing.T) {
	var hits atomic.Int32
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte{0xff, 0xd8, 0xff, 0xd9}) }))
	defer device.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	db, err := store.New(filepath.Join(t.TempDir(), "state.json"), key)
	if err != nil {
		t.Fatal(err)
	}
	var targets []model.ScheduleTarget
	for _, name := range []string{"first", "second"} {
		c, err := db.PutCamera(model.Camera{Name: name, BaseURL: device.URL, Username: "a", Password: "b", ChannelID: "101", DeviceType: "camera", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, model.ScheduleTarget{CameraID: c.ID})
	}
	count, err := New(db, 2).RunSchedule(context.Background(), model.Schedule{Targets: targets}, "test")
	if count != 0 || err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") || hits.Load() != 2 {
		t.Fatalf("count=%d err=%v hits=%d", count, err, hits.Load())
	}
}
