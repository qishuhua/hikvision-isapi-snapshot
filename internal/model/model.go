package model

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Camera struct {
	ID           string `json:"id"`
	DeviceType   string `json:"device_type,omitempty"`
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	Username     string `json:"username"`
	Password     string `json:"password,omitempty"`
	HasPassword  bool   `json:"has_password,omitempty"`
	ChannelID    string `json:"channel_id"`
	EndpointMode string `json:"endpoint_mode"` // streaming or proxy
	Enabled      bool   `json:"enabled"`
	Channels     string `json:"channels,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	RetryCount   int    `json:"retry_count,omitempty"`
}

type Storage struct {
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	Bucket             string `json:"bucket"`
	Prefix             string `json:"prefix"`
	AccessKeyID        string `json:"access_key_id"`
	SecretAccessKey    string `json:"secret_access_key,omitempty"`
	SessionToken       string `json:"session_token,omitempty"`
	HasSecretAccessKey bool   `json:"has_secret_access_key,omitempty"`
	HasSessionToken    bool   `json:"has_session_token,omitempty"`
	ClearSessionToken  bool   `json:"clear_session_token,omitempty"`
	ForcePathStyle     bool   `json:"force_path_style"` // only for non-OSS S3-compatible targets
}

type ScheduleTarget struct {
	CameraID       string `json:"camera_id"`
	TargetChannels string `json:"target_channels,omitempty"`
}

type Schedule struct {
	Targets         []ScheduleTarget `json:"targets,omitempty"`
	ID              string           `json:"id"`
	CameraID        string           `json:"camera_id"`
	TargetChannels  string           `json:"target_channels,omitempty"`
	Name            string           `json:"name"`
	Kind            string           `json:"kind"` // 间隔或每日计划
	IntervalSeconds int              `json:"interval_seconds,omitempty"`
	Times           []string         `json:"times,omitempty"` // HH:MM or HH:MM:SS
	Timezone        string           `json:"timezone,omitempty"`
	RunAt           string           `json:"run_at,omitempty"` // 保留旧计划中的时间字段
	Enabled         bool             `json:"enabled"`
	LastSlot        string           `json:"last_slot,omitempty"`
}

type CaptureRecord struct {
	ID         string    `json:"id"`
	CameraID   string    `json:"camera_id"`
	CameraName string    `json:"camera_name"`
	ScheduleID string    `json:"schedule_id,omitempty"`
	Source     string    `json:"source"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Bytes      int       `json:"bytes,omitempty"`
	ObjectKey  string    `json:"object_key,omitempty"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

func ValidateCamera(c *Camera) error {
	if c.DeviceType != "" && c.DeviceType != "nvr" && c.DeviceType != "camera" {
		return errors.New("device_type must be nvr or camera")
	}
	if len(strings.TrimSpace(c.Name)) == 0 || len(c.Name) > 100 {
		return errors.New("camera name is required (max 100 chars)")
	}
	if len(c.Username) == 0 {
		return errors.New("camera username is required")
	}
	if c.EndpointMode == "" {
		c.EndpointMode = "streaming"
	}
	if c.EndpointMode != "streaming" && c.EndpointMode != "proxy" {
		return errors.New("endpoint_mode must be streaming or proxy")
	}
	if c.Channels != "" {
		if c.EndpointMode != "streaming" {
			return errors.New("multi-channel collection requires streaming mode")
		}
		if c.Channels != "all" {
			for _, id := range strings.Split(c.Channels, ",") {
				n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32)
				if err != nil || n == 0 {
					return errors.New("channels must be all or comma-separated positive stream IDs")
				}
			}
		}
	}
	if c.Width < 0 || c.Height < 0 || (c.Width == 0) != (c.Height == 0) || c.RetryCount < 0 || c.RetryCount > 10 {
		return errors.New("invalid resolution or retry count")
	}
	if len(c.ChannelID) == 0 || len(c.ChannelID) > 12 {
		return errors.New("channel_id required")
	}
	if _, err := strconv.ParseUint(c.ChannelID, 10, 32); err != nil {
		return errors.New("channel_id must be a positive number")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("base_url must be http(s)://host[:port] without a path or credentials")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return nil
}

func ValidateStorage(s *Storage) error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("storage endpoint must be an https:// hostname without path")
	}
	if len(s.Region) == 0 || len(s.Region) > 64 || len(s.Bucket) == 0 || len(s.Bucket) > 63 {
		return errors.New("region and bucket are required")
	}
	if strings.ContainsAny(s.Bucket, "/ :") || strings.ContainsAny(s.Region, "/ :") {
		return errors.New("invalid region or bucket")
	}
	if s.AccessKeyID == "" {
		return errors.New("access_key_id is required")
	}
	if strings.HasPrefix(s.Prefix, "/") || strings.Contains(s.Prefix, "..") {
		return errors.New("prefix must be relative and cannot contain '..'")
	}
	return nil
}

func (j Schedule) EffectiveTargets() []ScheduleTarget {
	if len(j.Targets) > 0 {
		return j.Targets
	}
	if j.CameraID != "" {
		return []ScheduleTarget{{CameraID: j.CameraID, TargetChannels: j.TargetChannels}}
	}
	return nil
}

func ValidateSchedule(j *Schedule) error {
	targets := j.EffectiveTargets()
	if len(targets) == 0 {
		return errors.New("at least one target is required")
	}
	seenTargets := map[string]bool{}
	for _, target := range targets {
		if target.CameraID == "" || seenTargets[target.CameraID] {
			return errors.New("target camera_id must be unique and nonempty")
		}
		seenTargets[target.CameraID] = true
		if target.TargetChannels != "" && target.TargetChannels != "all" {
			for _, id := range strings.Split(target.TargetChannels, ",") {
				n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32)
				if err != nil || n == 0 {
					return errors.New("invalid target_channels")
				}
			}
		}
	}
	if j.Name == "" {
		j.Name = j.Kind
	}
	switch j.Kind {
	case "interval":
		if j.IntervalSeconds < 10 || j.IntervalSeconds > 86400 {
			return errors.New("interval_seconds must be 10..86400")
		}
	case "daily":
		if len(j.Times) == 0 || len(j.Times) > 24 {
			return errors.New("daily times required (max 24)")
		}
		if j.Timezone == "" {
			j.Timezone = "Asia/Shanghai"
		}
		if _, err := time.LoadLocation(j.Timezone); err != nil {
			return fmt.Errorf("invalid timezone: %w", err)
		}
		seen := map[string]bool{}
		for _, t := range j.Times {
			if _, err := ParseClock(t); err != nil {
				return fmt.Errorf("invalid time %q: %w", t, err)
			}
			if seen[t] {
				return errors.New("duplicate daily time")
			}
			seen[t] = true
		}
	default:
		return errors.New("kind must be interval or daily")
	}
	return nil
}

func ParseClock(s string) (time.Duration, error) {
	formats := []string{"15:04", "15:04:05"}
	for _, layout := range formats {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second, nil
		}
	}
	return 0, errors.New("use HH:MM or HH:MM:SS")
}

func DueSlot(j Schedule, now time.Time) (string, bool) {
	if !j.Enabled {
		return "", false
	}
	switch j.Kind {
	case "interval":
		if j.IntervalSeconds < 10 {
			return "", false
		}
		slot := fmt.Sprintf("interval:%d", now.Unix()/int64(j.IntervalSeconds))
		return slot, slot != j.LastSlot
	case "daily":
		loc, err := time.LoadLocation(j.Timezone)
		if err != nil {
			return "", false
		}
		local := now.In(loc)
		day := local.Format("2006-01-02")
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		var newest string
		var newestTime time.Time
		for _, clock := range j.Times {
			d, err := ParseClock(clock)
			if err != nil {
				continue
			}
			target := midnight.Add(d)
			if !now.Before(target) && now.Before(target.Add(time.Minute)) && (newest == "" || target.After(newestTime)) {
				newest = "daily:" + day + ":" + clock
				newestTime = target
			}
		}
		if newest != "" {
			return newest, newest != j.LastSlot
		}

	}
	return "", false
}
