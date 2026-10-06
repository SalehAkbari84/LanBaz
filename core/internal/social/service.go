package social

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nip59"
)

// Event kinds. The profile is an addressable application-data event; every
// message is a NIP-59 gift wrap around a rumor of innerKind.
const (
	profileKind = nostr.KindApplicationSpecificData // 30078
	innerKind   = 21420
	profileTag  = "lanbaz:"
)

// Online is how long a friend counts as online after their last message.
const Online = 3 * time.Minute

// presenceEvery is how often presence is sent to every friend.
const presenceEvery = 60 * time.Second

// Profile is what a friend learns about us.
type Profile struct {
	Name   string `json:"name"`
	PeerID string `json:"peer"`
	EdKey  string `json:"ed"`
}

// Hooks connect the service to the rest of the daemon.
type Hooks struct {
	// Profile returns the current display name and LanBaz identity.
	Profile func() Profile
	// Presence fills the room/game part of a presence message.
	Presence func(*Message)
	// Changed is called whenever the friend list or a friend's presence
	// changes; the daemon turns it into an API event.
	Changed func(f Friend, why string)
	// Join receives every join_* message from a confirmed friend.
	Join func(from Friend, m Message)
}

// Service is the friend system of one LanBaz installation.
type Service struct {
	keys   Keys
	signer keyer.KeySigner
	bus    Bus
	store  *Store
	hooks  Hooks
	log    *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	seen      map[string]time.Time // message ids already handled
	strangers map[string]time.Time // last friend_request per unknown sender
	burst     []time.Time          // friend_requests from strangers, last minute
}

// New builds a service. It does nothing on the network until Run.
func New(keys Keys, bus Bus, store *Store, hooks Hooks, log *slog.Logger) (*Service, error) {
	signer, err := keyer.NewPlainKeySigner(keys.Secret)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		keys: keys, signer: signer, bus: bus, store: store, hooks: hooks, log: log,
		now: time.Now, seen: map[string]time.Time{}, strangers: map[string]time.Time{},
	}, nil
}

// Code is this installation's friend code.
func (s *Service) Code() string { return FriendCode(s.keys.Public) }

// PublicKey is this installation's Nostr public key.
func (s *Service) PublicKey() string { return s.keys.Public }

// Run publishes the profile, listens for messages and sends presence until
// ctx is done.
func (s *Service) Run(ctx context.Context) {
	s.publishProfile(ctx)
	since := nostr.Timestamp(s.now().Add(-maxAgeFriend - 7*time.Hour).Unix())
	in := s.bus.Subscribe(ctx, nostr.Filter{
		Kinds: []int{nostr.KindGiftWrap},
		Tags:  nostr.TagMap{"p": []string{s.keys.Public}},
		Since: &since,
	})
	tick := time.NewTicker(presenceEvery)
	defer tick.Stop()
	profileTick := time.NewTicker(6 * time.Hour)
	defer profileTick.Stop()
	go s.sendPresence(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			s.handle(ctx, ev)
		case <-tick.C:
			go s.sendPresence(ctx)
		case <-profileTick.C:
			go s.publishProfile(ctx)
		}
	}
}

// PublishProfile re-announces the profile, e.g. after a name change.
func (s *Service) PublishProfile(ctx context.Context) { s.publishProfile(ctx) }

func (s *Service) publishProfile(ctx context.Context) {
	p := s.profile()
	content, _ := json.Marshal(p)
	ev := nostr.Event{
		Kind:      profileKind,
		CreatedAt: nostr.Timestamp(s.now().Unix()),
		Tags:      nostr.Tags{{"d", profileTag + s.Code()}},
		Content:   string(content),
	}
	if err := s.signer.SignEvent(ctx, &ev); err != nil {
		return
	}
	if err := s.bus.Publish(ctx, ev); err != nil {
		s.log.Warn("could not publish the friend profile", "error", err)
	}
}

func (s *Service) profile() Profile {
	if s.hooks.Profile != nil {
		return s.hooks.Profile()
	}
	return Profile{}
}

// Resolve finds who owns a friend code. The relay's answer is only believed
// when its key hashes back to the code.
func (s *Service) Resolve(ctx context.Context, code string) (string, Profile, error) {
	code = NormalizeCode(code)
	if code == "" {
		return "", Profile{}, errors.New("that is not a LanBaz friend code (LBZ-XXXXX-XXXXX)")
	}
	evs := s.bus.Query(ctx, nostr.Filter{
		Kinds: []int{profileKind},
		Tags:  nostr.TagMap{"d": []string{profileTag + code}},
	})
	var best *nostr.Event
	for i := range evs {
		e := &evs[i]
		if FriendCode(e.PubKey) != code {
			continue // squatter
		}
		if ok, _ := e.CheckSignature(); !ok {
			continue
		}
		if best == nil || e.CreatedAt > best.CreatedAt {
			best = e
		}
	}
	if best == nil {
		return "", Profile{}, errors.New("nobody with that friend code was found; ask your friend to open LanBaz once")
	}
	var p Profile
	_ = json.Unmarshal([]byte(best.Content), &p)
	if !textOK(p.Name, 40) || !idShape(p.PeerID) || len(p.EdKey) > 64 {
		p = Profile{}
	}
	return best.PubKey, p, nil
}

// Add sends a friend request to the owner of code. If they already asked us,
// this accepts instead.
func (s *Service) Add(ctx context.Context, code string) (Friend, error) {
	pub, p, err := s.Resolve(ctx, code)
	if err != nil {
		return Friend{}, err
	}
	if pub == s.keys.Public {
		return Friend{}, errors.New("that is your own friend code")
	}
	if f, ok := s.store.get(pub); ok && f.State == StatePendingIn {
		return s.Respond(ctx, pub, true)
	}
	f, _ := s.store.update(pub, true, func(f *Friend) {
		if f.State != StateFriend {
			f.State = StatePendingOut
		}
		if p.Name != "" {
			f.Name = p.Name
		}
		f.PeerID, f.EdKey = p.PeerID, p.EdKey
	})
	s.changed(f, "added")
	return f, s.sendProfiled(ctx, pub, TypeFriendRequest)
}

// Respond accepts or declines a pending friend request.
func (s *Service) Respond(ctx context.Context, pub string, accept bool) (Friend, error) {
	f, ok := s.store.get(pub)
	if !ok || f.State != StatePendingIn {
		return Friend{}, errors.New("there is no pending request from that player")
	}
	if !accept {
		s.store.remove(pub)
		f.State = ""
		s.changed(f, "declined")
		return f, nil
	}
	f, _ = s.store.update(pub, false, func(f *Friend) { f.State = StateFriend })
	s.changed(f, "accepted")
	err := s.sendProfiled(ctx, pub, TypeFriendAccept)
	go s.presenceTo(context.WithoutCancel(ctx), pub)
	return f, err
}

// Remove ends a friendship (or withdraws a request) and tells the other side.
func (s *Service) Remove(ctx context.Context, pub string) error {
	f, ok := s.store.get(pub)
	if !ok {
		return errors.New("not in your friend list")
	}
	s.store.remove(pub)
	f.State = ""
	s.changed(f, "removed")
	return s.Send(ctx, pub, Message{Type: TypeFriendRemove})
}

// SetTrusted turns auto-accept of join requests on or off for a friend.
func (s *Service) SetTrusted(pub string, trusted bool) (Friend, error) {
	f, ok := s.store.update(pub, false, func(f *Friend) { f.Trusted = trusted })
	if !ok || f.State != StateFriend {
		return Friend{}, errors.New("not a friend")
	}
	s.changed(f, "trusted")
	return f, nil
}

// List returns the friend list.
func (s *Service) List() []Friend { return s.store.list() }

// Get returns one entry.
func (s *Service) Get(pub string) (Friend, bool) { return s.store.get(pub) }

// IsOnline reports whether a friend was heard from recently.
func (s *Service) IsOnline(f Friend) bool {
	return !f.LastSeen.IsZero() && s.now().Sub(f.LastSeen) < Online
}

func (s *Service) sendProfiled(ctx context.Context, pub, typ string) error {
	p := s.profile()
	return s.Send(ctx, pub, Message{Type: typ, Name: p.Name, PeerID: p.PeerID, EdKey: p.EdKey})
}

// Send encrypts m for one recipient and publishes it.
func (s *Service) Send(ctx context.Context, pub string, m Message) error {
	if !validPub(pub) {
		return errors.New("bad recipient")
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	m.ID = hex.EncodeToString(id[:])
	m.At = s.now().Unix()
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	rumor := nostr.Event{
		Kind:      innerKind,
		CreatedAt: nostr.Timestamp(m.At),
		Tags:      nostr.Tags{},
		Content:   string(body),
		PubKey:    s.keys.Public,
	}
	expires := strconv.FormatInt(s.now().Add(lifetime(m.Type)).Unix(), 10)
	gw, err := nip59.GiftWrap(rumor, pub,
		func(plain string) (string, error) { return s.signer.Encrypt(ctx, plain, pub) },
		func(e *nostr.Event) error { return s.signer.SignEvent(ctx, e) },
		func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"expiration", expires}) },
	)
	if err != nil {
		return err
	}
	if err := s.bus.Publish(ctx, gw); err != nil {
		return fmt.Errorf("could not reach any relay: %w", err)
	}
	return nil
}

func (s *Service) handle(ctx context.Context, ev nostr.Event) {
	if ok, _ := ev.CheckSignature(); !ok {
		return
	}
	rumor, err := nip59.GiftUnwrap(ev, func(other, ct string) (string, error) {
		return s.signer.Decrypt(ctx, ct, other)
	})
	if err != nil || rumor.Kind != innerKind || !validPub(rumor.PubKey) || rumor.PubKey == s.keys.Public {
		return
	}
	m, err := decodeMessage(rumor.Content)
	if err != nil {
		return
	}
	now := s.now()
	if err := m.validate(now); err != nil {
		s.log.Debug("dropped a friend message", "error", err)
		return
	}
	if !s.firstTime(m.ID, now) {
		return
	}
	from := rumor.PubKey
	f, known := s.store.get(from)
	if !known {
		if m.Type != TypeFriendRequest || !s.allowStranger(from, now) {
			return
		}
		f, _ = s.store.update(from, true, func(f *Friend) {
			f.State = StatePendingIn
			f.Name, f.PeerID, f.EdKey = m.Name, m.PeerID, m.EdKey
		})
		s.changed(f, "request")
		return
	}
	switch m.Type {
	case TypeFriendRequest:
		switch f.State {
		case StatePendingOut:
			// Both sides added each other: that is consent from both.
			f, _ = s.store.update(from, false, func(f *Friend) {
				f.State = StateFriend
				f.Name, f.PeerID, f.EdKey = m.Name, m.PeerID, m.EdKey
			})
			s.changed(f, "accepted")
			go s.sendProfiled(context.WithoutCancel(ctx), from, TypeFriendAccept)
		case StateFriend:
			// They reinstalled or lost their list: answer again.
			go s.sendProfiled(context.WithoutCancel(ctx), from, TypeFriendAccept)
		}
	case TypeFriendAccept:
		if f.State == StatePendingOut || f.State == StateFriend {
			f, _ = s.store.update(from, false, func(f *Friend) {
				f.State = StateFriend
				if m.Name != "" {
					f.Name = m.Name
				}
				f.PeerID, f.EdKey = m.PeerID, m.EdKey
			})
			s.changed(f, "accepted")
			go s.presenceTo(context.WithoutCancel(ctx), from)
		}
	case TypeFriendRemove:
		s.store.remove(from)
		f.State = ""
		s.changed(f, "removed")
	case TypePresence:
		if f.State != StateFriend {
			return
		}
		f, _ = s.store.update(from, false, func(f *Friend) {
			f.LastSeen = now
			f.Presence = m
			if m.Name != "" {
				f.Name = m.Name
			}
		})
		s.changed(f, "presence")
	default: // join_*
		if f.State != StateFriend {
			return
		}
		f, _ = s.store.update(from, false, func(f *Friend) { f.LastSeen = now })
		if s.hooks.Join != nil {
			s.hooks.Join(f, m)
		}
	}
}

// firstTime records a message id and reports whether it is new.
func (s *Service) firstTime(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[id]; dup {
		return false
	}
	s.seen[id] = now
	if len(s.seen) > 5000 {
		for k, t := range s.seen {
			if now.Sub(t) > maxAgeFriend {
				delete(s.seen, k)
			}
		}
	}
	return true
}

// allowStranger rate-limits friend requests from people we do not know: one
// per sender per hour, twenty in total per minute.
func (s *Service) allowStranger(pub string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.strangers[pub]; ok && now.Sub(t) < time.Hour {
		return false
	}
	recent := s.burst[:0]
	for _, t := range s.burst {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.burst = recent
	if len(s.burst) >= 20 {
		return false
	}
	s.burst = append(s.burst, now)
	s.strangers[pub] = now
	return true
}

func (s *Service) sendPresence(ctx context.Context) {
	for _, f := range s.store.list() {
		if f.State == StateFriend {
			s.presenceTo(ctx, f.Pub)
		}
	}
}

func (s *Service) presenceTo(ctx context.Context, pub string) {
	m := Message{Type: TypePresence, Online: true, Name: s.profile().Name}
	if s.hooks.Presence != nil {
		s.hooks.Presence(&m)
	}
	if err := s.Send(ctx, pub, m); err != nil {
		s.log.Debug("presence not sent", "error", err)
	}
}

// AnnouncePresence sends presence to every friend now (after hosting, joining
// or a game change).
func (s *Service) AnnouncePresence(ctx context.Context) { go s.sendPresence(ctx) }

func (s *Service) changed(f Friend, why string) {
	if s.hooks.Changed != nil {
		s.hooks.Changed(f, why)
	}
}
