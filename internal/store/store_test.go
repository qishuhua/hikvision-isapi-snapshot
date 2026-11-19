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
