package social

// DefaultRelays are public Nostr relays from different operators. LanBaz
// publishes to all of them and listens on all of them, so one being down or
// blocked costs nothing.
var DefaultRelays = []string{
	"wss://relay.damus.io",
	"wss://nos.lol",
	"wss://relay.primal.net",
	"wss://offchain.pub",
	"wss://relay.snort.social",
}
