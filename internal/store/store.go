package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
)

type state struct {
	Cameras   map[string]model.Camera   `json:"cameras"`
	Schedules map[string]model.Schedule `json:"schedules"`
	Storage   model.Storage             `json:"storage"`
	Records   []model.CaptureRecord     `json:"records"`
}
type DB struct {
	mu    sync.Mutex
	path  string
	aead  cipher.AEAD
	state state
}

func New(path, keyBase64 string) (*DB, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil || len(key) != 32 {
		return nil, errors.New("DATA_ENCRYPTION_KEY must be base64 of exactly 32 random bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db := &DB{path: path, aead: aead, state: state{Cameras: map[string]model.Camera{}, Schedules: map[string]model.Schedule{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &db.state); err != nil {
		return nil, fmt.Errorf("invalid state file: %w", err)
	}
	if db.state.Cameras == nil {
		db.state.Cameras = map[string]model.Camera{}
	}
	if db.state.Schedules == nil {
		db.state.Schedules = map[string]model.Schedule{}
	}
	for id, c := range db.state.Cameras {
		if c.Password, err = db.decrypt(c.Password); err != nil {
			return nil, fmt.Errorf("decrypt camera %s: %w", id, err)
		}
		db.state.Cameras[id] = c
	}
	if db.state.Storage.SecretAccessKey, err = db.decrypt(db.state.Storage.SecretAccessKey); err != nil {
		return nil, fmt.Errorf("decrypt storage secret: %w", err)
	}
	if db.state.Storage.SessionToken, err = db.decrypt(db.state.Storage.SessionToken); err != nil {
		return nil, fmt.Errorf("decrypt storage token: %w", err)
	}
	return db, nil
}

func (d *DB) encrypt(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	nonce := make([]byte, d.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	encrypted := d.aead.Seal(nonce, nonce, []byte(s), nil)
	return "enc:" + base64.StdEncoding.EncodeToString(encrypted), nil
}
func (d *DB) decrypt(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if len(s) < 4 || s[:4] != "enc:" {
		return "", errors.New("secret is not encrypted; refusing to load")
	}
	raw, err := base64.StdEncoding.DecodeString(s[4:])
	if err != nil {
		return "", err
	}
	if len(raw) < d.aead.NonceSize() {
		return "", errors.New("invalid encrypted secret")
	}
	plain, err := d.aead.Open(nil, raw[:d.aead.NonceSize()], raw[d.aead.NonceSize():], nil)
	return string(plain), err
}
func (d *DB) saveLocked() error {
	copyState := state{Cameras: make(map[string]model.Camera, len(d.state.Cameras)), Schedules: d.state.Schedules, Records: d.state.Records, Storage: d.state.Storage}
	var err error
	for id, c := range d.state.Cameras {
		c.HasPassword = false
		c.Password, err = d.encrypt(c.Password)
		if err != nil {
			return err
		}
		copyState.Cameras[id] = c
	}
	copyState.Storage.HasSecretAccessKey = false
	copyState.Storage.HasSessionToken = false
	copyState.Storage.ClearSessionToken = false
	copyState.Storage.SecretAccessKey, err = d.encrypt(copyState.Storage.SecretAccessKey)
	if err != nil {
		return err
	}
	copyState.Storage.SessionToken, err = d.encrypt(copyState.Storage.SessionToken)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(copyState, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0700); err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func maskCamera(c model.Camera) model.Camera {
	c.HasPassword = c.Password != ""
	c.Password = ""
	return c
}
func maskStorage(s model.Storage) model.Storage {
	s.HasSecretAccessKey = s.SecretAccessKey != ""
	s.HasSessionToken = s.SessionToken != ""
	s.SecretAccessKey = ""
	s.SessionToken = ""
	return s
}
func (d *DB) ListCameras() []model.Camera {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]model.Camera, 0, len(d.state.Cameras))
	for _, c := range d.state.Cameras {
		out = append(out, maskCamera(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (d *DB) GetCamera(id string) (model.Camera, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.state.Cameras[id]
	return c, ok
}
func (d *DB) PutCamera(c model.Camera) (model.Camera, error) {
	if err := model.ValidateCamera(&c); err != nil {
		return model.Camera{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.ID == "" {
		c.ID = NewID()
	} else if old, ok := d.state.Cameras[c.ID]; ok && c.Password == "" {
		c.Password = old.Password
	}
	if c.Password == "" {
		return model.Camera{}, errors.New("camera password required")
	}
	d.state.Cameras[c.ID] = c
	if err := d.saveLocked(); err != nil {
		return model.Camera{}, err
	}
	return maskCamera(c), nil
}
func (d *DB) DeleteCamera(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.state.Cameras[id]; !ok {
		return os.ErrNotExist
	}
	delete(d.state.Cameras, id)
	for k, j := range d.state.Schedules {
		if len(j.Targets) == 0 {
			if j.CameraID == id {
				delete(d.state.Schedules, k)
			}
			continue
		}
		kept := make([]model.ScheduleTarget, 0, len(j.Targets))
		for _, target := range j.Targets {
			if target.CameraID != id {
				kept = append(kept, target)
			}
		}
		if len(kept) == 0 {
			delete(d.state.Schedules, k)
		} else {
			j.Targets = kept
			j.CameraID = ""
			j.TargetChannels = ""
			d.state.Schedules[k] = j
		}
	}
	return d.saveLocked()
}
func (d *DB) GetStorage() model.Storage    { d.mu.Lock(); defer d.mu.Unlock(); return d.state.Storage }
func (d *DB) PublicStorage() model.Storage { return maskStorage(d.GetStorage()) }
func (d *DB) PutStorage(s model.Storage) (model.Storage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.SecretAccessKey == "" {
		s.SecretAccessKey = d.state.Storage.SecretAccessKey
	}
	if s.ClearSessionToken {
		s.SessionToken = ""
	} else if s.SessionToken == "" {
		s.SessionToken = d.state.Storage.SessionToken
	}
	s.ClearSessionToken = false
	if err := model.ValidateStorage(&s); err != nil {
		return model.Storage{}, err
	}
	if s.SecretAccessKey == "" {
		return model.Storage{}, errors.New("secret_access_key required")
	}
	d.state.Storage = s
	if err := d.saveLocked(); err != nil {
		return model.Storage{}, err
	}
	return maskStorage(s), nil
}
func (d *DB) ListSchedules() []model.Schedule {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]model.Schedule, 0, len(d.state.Schedules))
	for _, j := range d.state.Schedules {
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (d *DB) GetSchedule(id string) (model.Schedule, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	j, ok := d.state.Schedules[id]
	return j, ok
}
func (d *DB) PutSchedule(j model.Schedule) (model.Schedule, error) {
	if err := model.ValidateSchedule(&j); err != nil {
		return model.Schedule{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, target := range j.EffectiveTargets() {
		c, ok := d.state.Cameras[target.CameraID]
		if !ok {
			return model.Schedule{}, errors.New("camera_id does not exist")
		}
		if target.TargetChannels != "" && c.DeviceType != "nvr" {
			return model.Schedule{}, errors.New("target_channels requires an NVR")
		}
		if len(j.Targets) > 0 && c.DeviceType == "nvr" && target.TargetChannels == "" {
			return model.Schedule{}, errors.New("NVR target requires channels")
		}
	}
	if j.ID == "" {
		j.ID = NewID()
	} // editing resets last slot so next future cycle is eligible
	j.LastSlot = ""
	d.state.Schedules[j.ID] = j
	if err := d.saveLocked(); err != nil {
		return model.Schedule{}, err
	}
	return j, nil
}
func (d *DB) DeleteSchedule(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.state.Schedules[id]; !ok {
		return os.ErrNotExist
	}
	delete(d.state.Schedules, id)
	return d.saveLocked()
}
func (d *DB) ClaimDue(now time.Time, limit int) ([]model.Schedule, error) {
	if limit <= 0 {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	jobs := make([]model.Schedule, 0)
	ids := make([]string, 0, len(d.state.Schedules))
	for id := range d.state.Schedules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if len(jobs) >= limit {
			break
		}
		j := d.state.Schedules[id]
		enabled := false
		for _, target := range j.EffectiveTargets() {
			if c, ok := d.state.Cameras[target.CameraID]; ok && c.Enabled {
				enabled = true
				break
			}
		}
		if !enabled {
			continue
		}
		slot, due := model.DueSlot(j, now)
		if !due {
			continue
		}
		j.LastSlot = slot
		d.state.Schedules[id] = j
		jobs = append(jobs, j)
	}
	if len(jobs) > 0 {
		if err := d.saveLocked(); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}
func (d *DB) AddRecord(r model.CaptureRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state.Records = append([]model.CaptureRecord{r}, d.state.Records...)
	if len(d.state.Records) > 200 {
		d.state.Records = d.state.Records[:200]
	}
	return d.saveLocked()
}
func (d *DB) ListRecords(limit int) []model.CaptureRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if limit > len(d.state.Records) {
		limit = len(d.state.Records)
	}
	return append([]model.CaptureRecord{}, d.state.Records[:limit]...)
}
