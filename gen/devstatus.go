package gen

import (
	"encoding/json"
	"time"
)

// devStatus is the dev-loop state pushed to the browser panel as an
// {"event":"status"} payload on /__gsx/event. Field names are the wire
// contract with vite-plugin-gsx.
type devStatus struct {
	Phase string `json:"phase"` // idle | generating | building | starting
	// PhaseSince is when the current Phase started (set by setPhase in
	// runDev, alongside every Phase transition). Always present — never the
	// zero value once runDev's initial status literal has run — so the
	// panel can render elapsed time ("building… started 42s ago") without
	// additional polling.
	PhaseSince time.Time  `json:"phaseSince"`
	Server     serverStat `json:"server"`
	LastCycle  *cycleStat `json:"lastCycle,omitempty"`
	FrontDoor  frontStat  `json:"frontDoor"`
}

type serverStat struct {
	Healthy bool `json:"healthy"`
	// Port is derived from the resolved upstream's URL port (empty when the
	// upstream carries none), never from GO_PORT directly — kept for one
	// release so an older plugin's panel (rendering ":" + server.port) still
	// renders. See Upstream for the single source of truth.
	Port string `json:"port"`
	// Upstream is the resolved [dev].upstream origin (see resolveUpstream) —
	// the single source of truth for the dev backend the health probe hits
	// and the panel should display. The plugin renders
	// server.upstream ?? ":" + server.port.
	Upstream string `json:"upstream"`
}

type cycleStat struct {
	OK     bool      `json:"ok"`
	Errors int       `json:"errors"`
	At     time.Time `json:"at"`
	// DurationMs is how long this cycle took end to end (from cycle()'s
	// recorded start, or the initial-build path's own start, to this
	// terminal post), for the panel's "last: 2m10s" expectation-setting.
	DurationMs int64 `json:"durationMs"`
}

type frontStat struct {
	State    string `json:"state"` // up | restarting | given-up | external
	Restarts int    `json:"restarts"`
}

func statusEvent(s devStatus) []byte {
	b, _ := json.Marshal(struct {
		Event string `json:"event"`
		devStatus
	}{"status", s})
	return b
}
