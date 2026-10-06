package social

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/security"
)

// StoreFile is the friend list's file name in the state directory.
const StoreFile = "friends.json"

// Friend states.
const (
	StatePendingOut = "pending_out" // we asked, they have not answered
	StatePendingIn  = "pending_in"  // they asked, we have not answered
	StateFriend     = "friend"
)

// Friend is one entry of the friend list.
type Friend struct {
	Pub     string    `json:"pub"`
	Code    string    `json:"code"`
	Name    string    `json:"name"`
	PeerID  string    `json:"peer,omitempty"`
	EdKey   string    `json:"ed,omitempty"`
	State   string    `json:"state"`
	Trusted bool      `json:"trusted,omitempty"`
	Since   time.Time `json:"since"`

	// Presence, not persisted.
	LastSeen time.Time `json:"-"`
	Presence Message   `json:"-"`
}

// Store is the friend list, persisted as JSON with an owner-only ACL.
type Store struct {
	path string
	mu   sync.Mutex
	m    map[string]*Friend
}

// OpenStore loads the friend list from the state directory.
func OpenStore(stateDir string) (*Store, error) {
	s := &Store{path: filepath.Join(stateDir, StoreFile), m: map[string]*Friend{}}
	if stateDir == "" {
		s.path = ""
		return s, nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Friend
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	for _, f := range list {
		if validPub(f.Pub) {
			s.m[f.Pub] = f
		}
	}
	return s, nil
}

// get returns a copy of a friend.
func (s *Store) get(pub string) (Friend, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.m[pub]
	if !ok {
		return Friend{}, false
	}
	return *f, true
}

// update applies fn to a friend (creating it when create is true) and saves.
func (s *Store) update(pub string, create bool, fn func(f *Friend)) (Friend, bool) {
	s.mu.Lock()
	f, ok := s.m[pub]
	if !ok {
		if !create {
			s.mu.Unlock()
			return Friend{}, false
		}
		f = &Friend{Pub: pub, Code: FriendCode(pub), Since: time.Now().UTC()}
		s.m[pub] = f
	}
	before := *f
	fn(f)
	out := *f
	persist := before.State != f.State || before.Name != f.Name || before.Trusted != f.Trusted ||
		before.PeerID != f.PeerID || before.EdKey != f.EdKey || !ok
	s.mu.Unlock()
	if persist {
		s.save()
	}
	return out, true
}

func (s *Store) remove(pub string) bool {
	s.mu.Lock()
	_, ok := s.m[pub]
	delete(s.m, pub)
	s.mu.Unlock()
	if ok {
		s.save()
	}
	return ok
}

// list returns every entry, friends first, then by name.
func (s *Store) list() []Friend {
	s.mu.Lock()
	out := make([]Friend, 0, len(s.m))
	for _, f := range s.m {
		out = append(out, *f)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == StateFriend) != (out[j].State == StateFriend) {
			return out[i].State == StateFriend
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Store) save() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	list := make([]*Friend, 0, len(s.m))
	for _, f := range s.m {
		c := *f
		list = append(list, &c)
	}
	s.mu.Unlock()
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = security.RestrictToOwner(s.path)
}
