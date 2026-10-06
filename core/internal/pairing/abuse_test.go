package pairing

import "testing"

// An invite carries its own signing secret, so anyone can mint one that
// verifies. The room id becomes the virtual adapter's name and reaches
// elevated PowerShell; a typographic quote (U+2019) closes a single-quoted
// PowerShell string, so this id would have run calc as administrator on the
// guest's PC. Such codes must be refused before any field is used.
func TestVerifyRejectsForgedIdentifiers(t *testing.T) {
	for _, bad := range []struct{ field, value string }{
		{"room", "lbzroom-x’;calc#"},
		{"room", "lbzroom-x';calc#"},
		{"room", "LanBaz-*"},
		{"guest", "guest\nid"},
		{"host", "host id"},
	} {
		c := newTestCode(t)
		switch bad.field {
		case "room":
			c.RoomID = bad.value
		case "guest":
			c.GuestID = bad.value
		case "host":
			c.HostID = bad.value
		}
		if err := c.Sign(); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if err := c.Verify(); err == nil {
			t.Errorf("a code with %s id %q was accepted", bad.field, bad.value)
		}
	}
	if err := newTestCode(t).Verify(); err != nil {
		t.Fatalf("a well-formed code was rejected: %v", err)
	}
}
