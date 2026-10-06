package daemon

import (
	"context"
	"reflect"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/games"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
	"github.com/lanbaz/lanbaz/profiles"
)

// detectEvery is how often the process list is checked. Starting a game and
// opening a world takes seconds, so three is quick enough to feel instant
// and cheap enough to never show up in Task Manager.
const detectEvery = 3 * time.Second

// gameService backs game.list and game.detect.
type gameService struct {
	d       *Daemon
	library []protocol.GameInfo
}

func (g gameService) List() []protocol.GameInfo { return g.library }

func (g gameService) Detect() []protocol.Presence {
	if r := g.d.Rooms(); r != nil {
		return r.Presences()
	}
	return nil
}

// initGames loads the bundled profiles and registers the game API.
func (d *Daemon) initGames() error {
	ps, errs := profiles.Load()
	for _, err := range errs {
		d.log.Warn("a game profile was skipped", "error", err)
	}
	library := make([]protocol.GameInfo, 0, len(ps))
	for _, p := range ps {
		library = append(library, protocol.GameInfo{
			ID:        p.ID,
			Name:      p.Name,
			Protocol:  p.Network.Protocol,
			Ports:     p.Network.Ports,
			Discovery: p.Discovery.Type,
			JoinHint:  p.JoinHint,
			NeedsL2:   p.NeedsL2,
			Notes:     p.Notes,

			Availability: p.Availability,
			Requires:     p.Requires,
			MaxLanRTTMs:  p.MaxLanRTTMs,
		})
	}
	d.detector = games.New(ps)
	d.profiles = ps
	d.log.Info("game profiles loaded", "count", len(ps))
	return d.api.SetGameService(gameService{d: d, library: library})
}

// watchGames runs the detector and announces changes to every room.
func (d *Daemon) watchGames(ctx context.Context) {
	if d.detector == nil {
		return
	}
	ticker := time.NewTicker(detectEvery)
	defer ticker.Stop()
	var last *room.LocalGame
	var lastFirewallExe string
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case <-ticker.C:
		}
		var next *room.LocalGame
		var gamePath string
		if det, ok := d.detector.Detect(); ok {
			gamePath = det.Path
			next = &room.LocalGame{GameID: det.ID(), GameName: det.Name(), Exe: det.Exe, Ports: det.Ports}
			if det.Profile != nil {
				prof := *det.Profile
				next.JoinURI = prof.JoinURIFor
			}
		}
		d.gameRunning.Store(next != nil)
		d.checkDiscovery(next != nil)
		if sameGame(last, next) {
			continue
		}
		last = next
		if next != nil && next.Exe != lastFirewallExe {
			lastFirewallExe = next.Exe
			go d.checkGameFirewall(ctx, next.GameID, next.GameName, gamePath)
		} else if next == nil {
			lastFirewallExe = ""
		}
		if next != nil {
			d.log.Info("game detected", "game", next.GameName, "hosting", len(next.Ports) > 0, "ports", next.Ports)
		} else {
			d.log.Info("no game running")
		}
		if rooms := d.Rooms(); rooms != nil {
			rooms.SetLocalGame(next)
		}
		if d.friends != nil {
			name := ""
			if next != nil {
				name = next.GameName
			}
			d.friends.setGame(name)
		}
		self := protocol.Presence{PeerID: protocol.PeerID(d.id), Self: true, At: time.Now().UTC()}
		if next != nil {
			self.GameID, self.GameName, self.Exe, self.Hosting = next.GameID, next.GameName, next.Exe, len(next.Ports) > 0
		}
		d.api.PublishEvent(protocol.EventGameDetected, self)
	}
}

func sameGame(a, b *room.LocalGame) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.GameID == b.GameID && a.Exe == b.Exe && reflect.DeepEqual(a.Ports, b.Ports)
}

// checkDiscovery lets every room's network warn, in the developer log, when a
// game runs but none of its LAN discovery ever reaches LanBaz.
func (d *Daemon) checkDiscovery(gameRunning bool) {
	rooms := d.Rooms()
	if rooms == nil {
		return
	}
	for _, r := range rooms.All() {
		if c, ok := r.Network().(interface{ CheckDiscovery(bool) }); ok {
			c.CheckDiscovery(gameRunning)
		}
	}
}
