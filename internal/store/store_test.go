package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

func TestEncryptedPersistenceAndScheduleClaim(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	path := filepath.Join(t.TempDir(), "state.json")
	db, err := New(path, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.PutCamera(model.Camera{Name: "NVR", BaseURL: "http://192.168.0.2", Username: "admin", Password: "camera-secret", ChannelID: "201", EndpointMode: "proxy", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "" || !c.HasPassword {
		t.Fatalf("password exposed: %+v", c)
	}
	_, err = db.PutStorage(model.Storage{Endpoint: "https://s3.oss-cn-shanghai.aliyuncs.com", Region: "cn-shanghai", Bucket: "images-test", Prefix: "snapshots", AccessKeyID: "AK", SecretAccessKey: "oss-secret"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.PutSchedule(model.Schedule{CameraID: c.ID, Name: "every-minute", Kind: "interval", IntervalSeconds: 60, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claimed, err := db.ClaimDue(now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	claimed, err = db.ClaimDue(now, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("duplicate claim %+v %v", claimed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "camera-secret") || strings.Contains(string(raw), "oss-secret") {
		t.Fatal("secret on disk")
	}
	db2, err := New(path, key)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := db2.GetCamera(c.ID)
	if restored.Password != "camera-secret" {
		t.Fatal("camera password not restored")
	}
	if db2.GetStorage().SecretAccessKey != "oss-secret" {
		t.Fatal("OSS key not restored")
	}
	if len(db2.ListSchedules()) != 1 {
		t.Fatal("schedule missing")
	}
}
func TestMultiDeviceSchedulePersistsAndSurvivesDeletion(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	path := filepath.Join(t.TempDir(), "state.json")
	db, err := New(path, key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.PutCamera(model.Camera{Name: "nvr", BaseURL: "http://nvr", Username: "a", Password: "b", ChannelID: "101", DeviceType: "nvr", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.PutCamera(model.Camera{Name: "ipc", BaseURL: "http://ipc", Username: "a", Password: "b", ChannelID: "101", DeviceType: "camera", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.PutSchedule(model.Schedule{Name: "group", Kind: "interval", IntervalSeconds: 60, Enabled: true, Targets: []model.ScheduleTarget{{CameraID: first.ID, TargetChannels: "all"}, {CameraID: second.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	db, err = New(path, key)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ClaimDue(time.Now(), 10)
	if err != nil || len(jobs) != 1 || len(jobs[0].Targets) != 2 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if err = db.DeleteCamera(first.ID); err != nil {
		t.Fatal(err)
	}
	got, ok := db.GetSchedule(job.ID)
	if !ok || len(got.Targets) != 1 || got.Targets[0].CameraID != second.ID {
		t.Fatalf("schedule=%+v exists=%v", got, ok)
	}
	_, err = db.PutSchedule(model.Schedule{Kind: "once", RunAt: time.Now().Format(time.RFC3339), CameraID: second.ID})
	if err == nil {
		t.Fatal("one-off schedule accepted")
	}
}
