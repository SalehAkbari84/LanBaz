package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMeteredParsesTheProviderAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apiKey") != "secret-key" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[
			{"urls":"stun:stun.relay.metered.ca:80"},
			{"urls":"turn:global.relay.metered.ca:80","username":"u","credential":"c"},
			{"urls":["turn:global.relay.metered.ca:443?transport=tcp","turns:global.relay.metered.ca:443?transport=tcp"],"username":"u","credential":"c"}
		]`))
	}))
	defer srv.Close()
	old := meteredBase
	meteredBase = srv.URL + "/%s?apiKey=%s"
	defer func() { meteredBase = old }()

	got, err := Metered(context.Background(), "myapp", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].URL != "turns:global.relay.metered.ca:443?transport=tcp" || got[0].Username != "u" {
		t.Fatalf("got %+v", got)
	}
	if _, err := Metered(context.Background(), "myapp", "wrong"); err == nil {
		t.Fatal("a rejected key was not reported")
	}
}
