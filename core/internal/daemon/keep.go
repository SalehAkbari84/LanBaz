package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Kept networks: the Radmin/Hamachi model on top of rooms. A host marks its
// room "keep": it is reopened whenever LanBaz starts, and the friends who were
// in it are let back in without a prompt. A guest marks a friend's room
// "keep": whenever that friend is online and LanBaz is not in their room, it
// asks to join again. After a reboot, sleep or IP change both sides find each
// other with no clicks. Only rooms between friends can be kept - a friend is
// the stable identity that survives a new room id and a new pairing code.

const keptFile = "networks.json"

// Variables so tests can run the loop faster.
var (
	keepEvery    = 15 * time.Second
	keepAskEvery = time.Minute
)

type keptHost struct {
	Name    string   `json:"name"`
	Mode    string   `json:"mode,omitempty"`
	Members []string `json:"members,omitempty"` // friend pubs let back in without a prompt
}

type keptState struct {
	Host *keptHost `json:"host,omitempty"`
	Join []string  `json:"join,omitempty"` // host friend pubs whose room we keep joining
}

type keeper struct {
	path string

	mu       sync.Mutex
	st       keptState
	hostRoom string               // the open room serving st.Host
	askedAt  map[string]time.Time // host pub -> last automatic join request
	failed   string               // last reopen error, logged once
}

func newKeeper(stateDir string) *keeper {
	k := &keeper{askedAt: map[string]time.Time{}}
	if stateDir == "" {
		return k
	}
	k.path = filepath.Join(stateDir, keptFile)
	if b, err := os.ReadFile(k.path); err == nil {
		_ = json.Unmarshal(b, &k.st)
	}
	return k
}

// save writes the state; the caller holds k.mu.
func (k *keeper) save() error {
	if k.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(k.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.path)
}

func (k *keeper) isMember(roomID, pub string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.st.Host != nil && roomID == k.hostRoom && slices.Contains(k.st.Host.Members, pub)
}

func (k *keeper) addMember(roomID, pub string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.st.Host == nil || roomID != k.hostRoom || slices.Contains(k.st.Host.Members, pub) {
		return
	}
	k.st.Host.Members = append(k.st.Host.Members, pub)
	_ = k.save()
}

func (k *keeper) joins(pub string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Contains(k.st.Join, pub)
}

// setKeep marks or unmarks a room. It reports what the room is now.
func (f *friends) setKeep(roomID string, keep bool) (protocol.KeptNetworks, error) {
	r, err := f.d.Rooms().Get(roomID)
	if err != nil {
		return protocol.KeptNetworks{}, err
	}
	k := f.keep
	f.mu.Lock()
	hostPub := f.hostOf[roomID]
	var members []string
	for pub := range f.accepted[roomID] {
		members = append(members, pub)
	}
	f.mu.Unlock()

	k.mu.Lock()
	switch {
	case r.IsHost() && keep:
		s := r.Summary()
		k.st.Host = &keptHost{Name: s.Name, Mode: r.Mode(), Members: members}
		k.hostRoom = roomID
	case r.IsHost():
		if k.hostRoom == roomID {
			k.st.Host, k.hostRoom = nil, ""
		}
	case hostPub == "":
		k.mu.Unlock()
		return protocol.KeptNetworks{}, protocol.NewError(protocol.CodeBadRequest,
			"only a room joined through a friend can be kept: the friend is how LanBaz finds the room again")
	case keep:
		if !slices.Contains(k.st.Join, hostPub) {
			k.st.Join = append(k.st.Join, hostPub)
		}
	default:
		k.st.Join = slices.DeleteFunc(k.st.Join, func(p string) bool { return p == hostPub })
	}
	err = k.save()
	k.mu.Unlock()
	if err != nil {
		return protocol.KeptNetworks{}, err
	}
	f.d.log.Info("kept network changed", "room", roomID, "keep", keep, "host", r.IsHost())
	return f.kept(), nil
}

// kept lists the open rooms that are kept and every friend whose room is.
func (f *friends) kept() protocol.KeptNetworks {
	k := f.keep
	k.mu.Lock()
	st, hostRoom := k.st, k.hostRoom
	join := slices.Clone(st.Join)
	k.mu.Unlock()

	out := protocol.KeptNetworks{Rooms: []string{}, Hosts: []protocol.KeptHost{}}
	if st.Host != nil {
		out.Hosting = &protocol.KeptHosting{Name: st.Host.Name, Mode: st.Host.Mode, Members: len(st.Host.Members)}
		if hostRoom != "" {
			out.Rooms = append(out.Rooms, hostRoom)
		}
	}
	f.mu.Lock()
	byHost := map[string]string{}
	for roomID, pub := range f.hostOf {
		byHost[pub] = roomID
	}
	f.mu.Unlock()
	for _, pub := range join {
		h := protocol.KeptHost{Pub: pub, RoomID: byHost[pub]}
		if fr, ok := f.svc.Get(pub); ok {
			h.Name, h.Online = fr.Name, f.svc.IsOnline(fr)
		}
		if h.RoomID != "" {
			out.Rooms = append(out.Rooms, h.RoomID)
		}
		out.Hosts = append(out.Hosts, h)
	}
	return out
}

// forgetOnLeave: leaving a room on purpose stops keeping it; closing because
// the host vanished, or LanBaz shutting down, does not.
func (f *friends) forgetOnLeave(roomID, hostPub, reason string) {
	if reason != "left" {
		return
	}
	k := f.keep
	k.mu.Lock()
	defer k.mu.Unlock()
	changed := false
	if roomID == k.hostRoom && k.st.Host != nil {
		k.st.Host, k.hostRoom, changed = nil, "", true
	}
	if hostPub != "" && slices.Contains(k.st.Join, hostPub) {
		k.st.Join = slices.DeleteFunc(k.st.Join, func(p string) bool { return p == hostPub })
		changed = true
	}
	if changed {
		_ = k.save()
		f.d.log.Info("stopped keeping a network you left", "room", roomID)
	}
}

// runKeep reopens the kept hosted room and rejoins kept friends' rooms.
func (f *friends) runKeep(ctx context.Context) {
	t := time.NewTicker(keepEvery)
	defer t.Stop()
	for {
		f.keepTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-f.d.shutdownCh:
			return
		case <-t.C:
		}
	}
}

func (f *friends) keepTick(ctx context.Context) {
	rooms := f.d.Rooms()
	if rooms == nil {
		return
	}
	k := f.keep
	k.mu.Lock()
	host := k.st.Host
	hostRoom := k.hostRoom
	join := slices.Clone(k.st.Join)
	k.mu.Unlock()

	if host != nil {
		if _, err := rooms.Get(hostRoom); hostRoom == "" || err != nil {
			resp, err := rooms.Create(ctx, protocol.RoomCreateRequest{Name: host.Name, Mode: host.Mode})
			k.mu.Lock()
			if err != nil {
				if k.failed != err.Error() {
					k.failed = err.Error()
					f.d.log.Warn("could not reopen your kept network; retrying", "name", host.Name, "error", err)
				}
			} else if k.st.Host != nil {
				k.hostRoom, k.failed = resp.Room.RoomID, ""
				f.d.log.Info("kept network reopened; friends who were in it are let back in automatically",
					"name", host.Name, "room", resp.Room.RoomID, "members", len(k.st.Host.Members))
			}
			k.mu.Unlock()
			if err == nil {
				f.svc.AnnouncePresence(ctx)
			}
		}
	}

	f.mu.Lock()
	guestRooms := map[string]string{} // room -> host pub
	for roomID, pub := range f.hostOf {
		guestRooms[roomID] = pub
	}
	f.mu.Unlock()
	inRoomOf := map[string]bool{}
	for roomID, pub := range guestRooms {
		r, err := rooms.Get(roomID)
		if err != nil {
			continue
		}
		if r.HostAlive() {
			inRoomOf[pub] = true
			continue
		}
		// The link to the host is down and the host is online in another
		// room: it restarted and reopened the network under a new id. The old
		// room can never come back; drop it so the new one can be joined.
		fr, ok := f.svc.Get(pub)
		if ok && f.svc.IsOnline(fr) && fr.Presence.Room != "" && fr.Presence.Room != roomID {
			f.d.log.Info("the host of a kept network restarted; moving to its new room", "friend", fr.Name, "old", roomID, "new", fr.Presence.Room)
			rooms.DropStale(roomID, "host_restarted")
			continue
		}
		inRoomOf[pub] = true // still recovering on its own
	}
	for _, pub := range join {
		fr, ok := f.svc.Get(pub)
		if !ok || fr.State != social.StateFriend || inRoomOf[pub] || !f.svc.IsOnline(fr) || fr.Presence.Room == "" {
			continue
		}
		k.mu.Lock()
		due := time.Since(k.askedAt[pub]) >= keepAskEvery
		if due {
			k.askedAt[pub] = time.Now()
		}
		k.mu.Unlock()
		if !due {
			continue
		}
		f.d.log.Info("rejoining a kept network", "friend", fr.Name, "room", fr.Presence.Room)
		_ = f.requestJoin(ctx, fr, fr.Presence.Room)
	}
}
