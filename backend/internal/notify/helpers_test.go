package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/notify/apns"
	"github.com/danielpina1/nexus/backend/internal/security"
)

var _ Directory = (*security.Security)(nil)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeDir struct {
	mu      sync.Mutex
	devices []*nexusv1.Device
	tokens  []security.LiveActivityToken
	dropped []string
	queries int
}

func (d *fakeDir) Devices(context.Context) ([]*nexusv1.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.devices, nil
}

func (d *fakeDir) LiveActivityTokens(_ context.Context, kind nexusv1.LiveActivityTokenKind, typ string) ([]security.LiveActivityToken, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries++
	var out []security.LiveActivityToken
	for _, t := range d.tokens {
		if (kind == 0 || t.Kind == kind) && (typ == "" || t.ActivityType == typ) {
			out = append(out, t)
		}
	}
	return out, nil
}

// droppingDir also implements PushTokenDropper / LiveActivityTokenDropper.
type droppingDir struct{ fakeDir }

func (d *droppingDir) DropPushToken(_ context.Context, dev, tok string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropped = append(d.dropped, "push:"+dev+":"+tok)
	return nil
}

func (d *droppingDir) DropLiveActivityToken(_ context.Context, dev string, tok []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropped = append(d.dropped, "la:"+dev)
	return nil
}

type recPusher struct {
	mu     sync.Mutex
	reqs   []apns.Request
	status func(r apns.Request) int
}

func (p *recPusher) Push(_ context.Context, r apns.Request) (*apns.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, r)
	st := 200
	if p.status != nil {
		st = p.status(r)
	}
	resp := &apns.Response{StatusCode: st}
	if st == 410 {
		resp.Reason = "Unregistered"
	}
	return resp, nil
}

func (p *recPusher) take() []apns.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.reqs
	p.reqs = nil
	return out
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func decodeAPS(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("payload: %v (%s)", err, payload)
	}
	aps, _ := m["aps"].(map[string]any)
	if aps == nil {
		t.Fatalf("no aps in %s", payload)
	}
	return aps
}
