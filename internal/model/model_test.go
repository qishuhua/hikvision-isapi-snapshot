package model

import (
	"testing"
	"time"
)

func TestDueSlot(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 31, 0, time.FixedZone("CST", 8*3600))
	j := Schedule{Kind: "daily", Times: []string{"09:00:00", "09:00:30"}, Timezone: "Asia/Shanghai", Enabled: true}
	slot, due := DueSlot(j, now)
	if !due || slot != "daily:2026-11-19:09:00:30" {
		t.Fatalf("slot %s due %v", slot, due)
	}
	j.LastSlot = slot
	if _, due = DueSlot(j, now.Add(time.Second)); due {
		t.Fatal("duplicate daily snapshot")
	}
	once := Schedule{Kind: "once", RunAt: now.Add(-time.Hour).Format(time.RFC3339), Enabled: true}
	if _, due = DueSlot(once, now); due {
		t.Fatal("legacy one-off plan must not execute")
	}
}
