// gen/devstatus.go
package gen

import (
	"encoding/json"
	"time"
)

// devStatus is the dev-loop state pushed to the browser panel as an
// {"event":"status"} payload on /__gsx/event. Field names are the wire
// contract with vite-plugin-gsx.
type devStatus struct {
	Phase     string     `json:"phase"` // idle | generating | building | starting
	Server    serverStat `json:"server"`
	LastCycle *cycleStat `json:"lastCycle,omitempty"`
	FrontDoor frontStat  `json:"frontDoor"`
}

type serverStat struct {
	Healthy bool   `json:"healthy"`
	Port    string `json:"port"`
}

type cycleStat struct {
	OK     bool      `json:"ok"`
	Errors int       `json:"errors"`
	At     time.Time `json:"at"`
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
