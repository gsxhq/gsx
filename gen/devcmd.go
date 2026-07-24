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
//
// token is sent as the x-gsx-token request header on every attempt. When the
// front door's plugin has a GSX_DEV_TOKEN configured it requires this header
// to match before releasing anything from the mailbox (else 403, without
// draining it); when unconfigured (externally-run vite, --no-web) the plugin
// ignores the header entirely, so sending it is always safe.
//
// onContact, when non-nil, is invoked exactly once — on the first successful
// response (200 or 204, the same paths that already reset backoff) seen
// across this call's lifetime. It never fires for transport errors, non-2xx
// statuses, or malformed bodies, and never fires again afterward. This is the
// earliest proof that the front door is actually serving our plugin (a
// managed vite's gate opens optimistically on process start, well before it
// necessarily answers requests — see frontDoor), used by runDev to re-post
// the current status once instead of leaving a warm-started panel waiting
// forever for a cycle that may never come.
func pollCommands(ctx context.Context, base, token string, up func() bool, out chan<- string, onContact func()) {
	if strings.HasPrefix(base, "/") || base == "" {
		return
	}
	client := devHTTPClient(30 * time.Second)
	backoff := time.Second
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	contacted := false
	signalContact := func() {
		if !contacted {
			contacted = true
			if onContact != nil {
				onContact()
			}
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
		req.Header.Set("x-gsx-token", token)
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
			signalContact()
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
		signalContact()
		for _, c := range payload.Cmds {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
	}
}
