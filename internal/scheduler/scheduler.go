package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/model"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/service"
)

type Scheduler struct {
	svc     *service.Service
	workers int
}

func New(svc *service.Service, workers int) *Scheduler {
	if workers < 1 {
		workers = 4
	}
	return &Scheduler{svc: svc, workers: workers}
}
func (s *Scheduler) Run(ctx context.Context) {
	queue := make(chan model.Schedule, s.workers*3)
	for i := 0; i < s.workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-queue:
					requestCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
					_, key, err := s.svc.CaptureSaved(requestCtx, job.CameraID, true, "schedule", job.ID)
					cancel()
					if err != nil {
						slog.Error("scheduled snapshot failed", "schedule_id", job.ID, "err", err)
					} else {
						slog.Info("scheduled snapshot uploaded", "schedule_id", job.ID, "object_key", key)
					}
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			available := cap(queue) - len(queue)
			jobs, err := s.svc.DB.ClaimDue(now, available)
			if err != nil {
				slog.Error("claim schedules", "err", err)
				continue
			}
			for _, job := range jobs {
				queue <- job
			}
		}
	}
}
