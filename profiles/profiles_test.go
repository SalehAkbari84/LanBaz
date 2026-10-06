package profiles

import (
	"testing"
)

// Every bundled profile must load: a profile with a hex literal or a trailing
// comma once shipped, and nothing noticed because nothing parsed it.
func TestBundledProfilesAreValid(t *testing.T) {
	ps, errs := Load()
	for _, err := range errs {
		t.Error(err)
	}
	if len(ps) < 20 {
		t.Fatalf("only %d profiles loaded", len(ps))
	}
	ids := map[string]bool{}
	for _, p := range ps {
		for _, a := range p.Availability {
			if a != "free" && a != "drm_free" {
				t.Errorf("%s: unknown availability %q", p.ID, a)
			}
		}
		if ids[p.ID] {
			t.Errorf("duplicate profile id %s", p.ID)
		}
		ids[p.ID] = true
	}
	// An executable shared by several games must be resolvable: at most one of
	// them may go without path hints (the fallback).
	for exe, cands := range Index(ps) {
		bare := 0
		for _, p := range cands {
			if len(p.PathHints) == 0 {
				bare++
			}
		}
		if len(cands) > 1 && bare > 1 {
			names := []string{}
			for _, p := range cands {
				names = append(names, p.ID)
			}
			t.Errorf("executable %s is shared by %v without path hints to tell them apart", exe, names)
		}
	}
}

func TestPathHintsTellGamesApart(t *testing.T) {
	ps, _ := Load()
	idx := Index(ps)
	zh, _ := Pick(idx["generals.exe"], `C:\Games\Command and Conquer Generals Zero Hour\generals.exe`)
	base, _ := Pick(idx["generals.exe"], `C:\Games\Command and Conquer Generals\generals.exe`)
	if zh.ID != "cnc-generals-zh" || base.ID != "cnc-generals" {
		t.Fatalf("picked %s and %s", zh.ID, base.ID)
	}
}

func TestJoinURIFillsTheTemplate(t *testing.T) {
	p := Profile{JoinURI: "steam://connect/{host}:{port}"}
	if got := p.JoinURIFor("10.200.5.1", 27015); got != "steam://connect/10.200.5.1:27015" {
		t.Fatalf("JoinURIFor = %q", got)
	}
	if got := (Profile{}).JoinURIFor("10.200.5.1", 1); got != "" {
		t.Fatalf("empty template gave %q", got)
	}
}
