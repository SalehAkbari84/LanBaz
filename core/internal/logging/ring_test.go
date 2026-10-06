package logging

import (
	"bytes"
	"testing"
)

func TestRingKeepsRecentRecordsAndRedacts(t *testing.T) {
	ring := NewRing(3)
	var got []Entry
	ring.OnEntry(func(e Entry) { got = append(got, e) })
	l, err := New(Options{Level: "info", Stderr: &bytes.Buffer{}, Ring: ring})
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("debug reaches the ring", "k", 1)
	l.Info("one")
	l.Warn("two", "token", "supersecretvalue")
	l.Error("three")
	all := ring.Since(0)
	if len(all) != 3 || all[0].Msg != "one" || all[2].Level != "ERROR" {
		t.Fatalf("ring holds %+v", all)
	}
	if len(got) != 4 || got[0].Level != "DEBUG" {
		t.Fatalf("callback saw %d records", len(got))
	}
	if after := ring.Since(all[1].Seq); len(after) != 1 || after[0].Msg != "three" {
		t.Fatalf("Since returned %+v", after)
	}
	for _, e := range all {
		if bytes.Contains([]byte(e.Attrs), []byte("supersecretvalue")) {
			t.Fatal("a token reached the developer log")
		}
	}
}
