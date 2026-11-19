package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/isapi"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/oss"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/store"
)

type Service struct {
	DB        *store.DB
	camera    *isapi.Client
	oss       *oss.Client
	semaphore chan struct{}
}

func New(db *store.DB, maxConcurrent int) *Service {
	if maxConcurrent < 1 {
		maxConcurrent = 4
	}
	return &Service{DB: db, camera: isapi.New(12 * time.Second), oss: oss.New(15 * time.Second), semaphore: make(chan struct{}, maxConcurrent)}
}
func (s *Service) Capture(ctx context.Context, c model.Camera, upload bool, source, scheduleID string) (jpeg []byte, objectKey string, err error) {
	started := time.Now()
	capturedBytes := 0
	recordKey := ""
	record := model.CaptureRecord{ID: store.NewID(), CameraID: c.ID, CameraName: c.Name, ScheduleID: scheduleID, Source: source, StartedAt: started}
	defer func() {
		record.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			record.Status = "failed"
			record.Error = err.Error()
			if len(record.Error) > 700 {
				record.Error = record.Error[:700]
			}
		} else {
			record.Status = "success"
		}
		record.Bytes = capturedBytes
		record.ObjectKey = recordKey
		if saveErr := s.DB.AddRecord(record); saveErr != nil {
			slog.Error("record save failed", "err", saveErr)
		}
	}()
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	jpeg, err = s.camera.Capture(ctx, c)
	if err != nil {
		return nil, "", fmt.Errorf("ISAPI capture: %w", err)
	}
	capturedBytes = len(jpeg)
	if upload {
		config := s.DB.GetStorage()
		if config.Endpoint == "" {
			return nil, "", fmt.Errorf("OSS storage is not configured")
		}
		id := c.ID
		if id == "" {
			id = "adhoc"
		}
		now := time.Now().UTC()
		prefix := strings.Trim(config.Prefix, "/")
		if prefix != "" {
			prefix += "/"
		}
		objectKey = fmt.Sprintf("%s%s/%s/%s_%s.jpg", prefix, now.Format("2006/01/02"), id, now.Format("20060102T150405.000Z"), store.NewID()[:8])
		recordKey = objectKey
		if err = s.oss.Upload(ctx, config, objectKey, jpeg); err != nil {
			return nil, "", fmt.Errorf("OSS upload: %w", err)
		}
	}
	return jpeg, objectKey, nil
}
func (s *Service) CaptureSaved(ctx context.Context, id string, upload bool, source, scheduleID string) ([]byte, string, error) {
	c, ok := s.DB.GetCamera(id)
	if !ok {
		return nil, "", fmt.Errorf("camera not found")
	}
	return s.Capture(ctx, c, upload, source, scheduleID)
}
