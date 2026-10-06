package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/lanbaz/lanbaz/core/internal/network"
)

type stubNet struct {
	network.VirtualNetwork
	checked []bool
	named   bool
}

func (s *stubNet) Start(context.Context) error                { return nil }
func (s *stubNet) Stop(context.Context) error                 { return nil }
func (s *stubNet) CheckDiscovery(b bool)                      { s.checked = append(s.checked, b) }
func (s *stubNet) SetNameSource(func() map[string]netip.Addr) { s.named = true }

// The wrapper must not hide the optional methods the room and the daemon find
// by interface assertion.
func TestGFWLWrapperForwards(t *testing.T) {
	inner := &stubNet{}
	v, err := withGFWL[*stubNet](func() string { return "LanBaz-test" }, slog.New(slog.NewTextHandler(io.Discard, nil)))(inner, nil)
	if err != nil {
		t.Fatal(err)
	}
	v.(interface{ CheckDiscovery(bool) }).CheckDiscovery(true)
	v.(interface {
		SetNameSource(func() map[string]netip.Addr)
	}).SetNameSource(nil)
	if len(inner.checked) != 1 || !inner.named {
		t.Fatalf("not forwarded: %+v", inner)
	}
	if _, ok := v.(interface{ Router() *network.Router }); !ok {
		t.Fatal("Router not exposed")
	}
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := v.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
