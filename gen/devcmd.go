package gen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// pollCommands long-polls base+/__gsx/cmd?wait=25 and delivers each command on
// out. up gates polling exactly like browser pushes: while the front door is
// down/unverified, no requests are made. Transport errors back off 1s→2s→5s
// (reset on success). Returns when ctx is done.
func pollCommands(ctx context.Context, base string, up func() bool, out chan<- string) {
	if strings.HasPrefix(base, "/") || base == "" {
		return
	}
	client := &http.Client{Timeout: 30 * time.Second}
	backoff := time.Second
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for ctx.Err() == nil {
		if !up() {
			if !sleep(time.Second) {
				return
			}
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/__gsx/cmd?wait=25", nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			if !sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			backoff = time.Second
			continue // idle long-poll: immediately re-poll
		}
		if resp.StatusCode != http.StatusOK {
			// Any other non-200 (500, 404, ...) is a failure, not an idle
			// poll: back off exactly like a transport error so an erroring
			// server can't be busy-spun against.
			if !sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		var payload struct {
			Cmds []string `json:"cmds"`
		}
		if json.Unmarshal(body, &payload) != nil {
			// Malformed body on a 200: same backoff treatment as a failure.
			if !sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = time.Second
		for _, c := range payload.Cmds {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
	}
}
