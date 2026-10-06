// Package relay fetches TURN relay servers from a hosted provider, so a user
// can turn on the fallback path with an account key instead of running a
// server. The relay only carries traffic when no direct path works; it is
// still encrypted end to end (DTLS), the provider sees opaque packets.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// meteredBase is overridden in tests.
var meteredBase = "https://%s.metered.live/api/v1/turn/credentials?apiKey=%s"

// Metered fetches the relay servers of a Metered.ca application. Only turn:
// and turns: URLs are kept; TURN over TCP and over TLS on port 443 are what get
// through routers and firewalls that block everything else.
func Metered(ctx context.Context, app, key string) ([]protocol.RelayServer, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := fmt.Sprintf(meteredBase, url.PathEscape(app), url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay: cannot reach Metered: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay: Metered answered %s (check the app name and API key)", resp.Status)
	}
	return parseICEServers(body)
}

// parseICEServers reads the RTCIceServer list a provider returns; "urls" may
// be a string or a list.
func parseICEServers(body []byte) ([]protocol.RelayServer, error) {
	var list []struct {
		URLs       json.RawMessage `json:"urls"`
		Username   string          `json:"username"`
		Credential string          `json:"credential"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("relay: unexpected answer from Metered: %w", err)
	}
	var out []protocol.RelayServer
	for _, s := range list {
		var urls []string
		var one string
		if json.Unmarshal(s.URLs, &one) == nil {
			urls = []string{one}
		} else {
			_ = json.Unmarshal(s.URLs, &urls)
		}
		for _, u := range urls {
			u = strings.TrimSpace(u)
			if strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:") {
				out = append(out, protocol.RelayServer{URL: u, Username: s.Username, Credential: s.Credential})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("relay: Metered returned no relay servers")
	}
	return out, nil
}
