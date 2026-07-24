package gen

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatusEventShape(t *testing.T) {
	at := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	since := time.Date(2026, 7, 23, 9, 59, 0, 0, time.UTC)
	s := devStatus{
		Phase:      "idle",
		PhaseSince: since,
		Server:     serverStat{Healthy: true, Port: "7777", Upstream: "http://localhost:7777"},
		LastCycle:  &cycleStat{OK: true, Errors: 0, At: at, DurationMs: 1234},
		FrontDoor:  frontStat{State: "up", Restarts: 2},
	}
	var got map[string]any
	if err := json.Unmarshal(statusEvent(s), &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "status" || got["phase"] != "idle" {
		t.Errorf("event/phase = %v/%v", got["event"], got["phase"])
	}
	if got["phaseSince"] != "2026-07-23T09:59:00Z" {
		t.Errorf("phaseSince = %v, want RFC3339 2026-07-23T09:59:00Z", got["phaseSince"])
	}
	srv := got["server"].(map[string]any)
	if srv["healthy"] != true || srv["port"] != "7777" || srv["upstream"] != "http://localhost:7777" {
		t.Errorf("server = %v", srv)
	}
	lc := got["lastCycle"].(map[string]any)
	if lc["ok"] != true || lc["at"] != "2026-07-23T10:00:00Z" {
		t.Errorf("lastCycle = %v", lc)
	}
	if durMs, ok := lc["durationMs"].(float64); !ok || durMs != 1234 {
		t.Errorf("lastCycle.durationMs = %v (%T), want 1234", lc["durationMs"], lc["durationMs"])
	}
	fd := got["frontDoor"].(map[string]any)
	if fd["state"] != "up" || fd["restarts"] != float64(2) {
		t.Errorf("frontDoor = %v", fd)
	}
}

func TestStatusEventOmitsNilLastCycle(t *testing.T) {
	var got map[string]any
	_ = json.Unmarshal(statusEvent(devStatus{Phase: "generating"}), &got)
	if _, present := got["lastCycle"]; present {
		t.Error("nil lastCycle must be omitted")
	}
}
