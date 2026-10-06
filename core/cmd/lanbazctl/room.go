package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The room and peer verbs.
//
// They exist so the whole of Phase 1 is reachable without the desktop shell. That
// is not only a convenience: it means the acceptance script can drive a real
// handshake end to end on a machine with no display, and it gives the UI a
// reference implementation of how a pairing code is supposed to be handled -
// issue it, show it, paste the reply, apply it.
//
// The longer default timeout here matters. room.create gathers ICE candidates,
// which takes seconds and cannot be shortened, so the global five second default
// would fail a perfectly healthy join.

const roomCallTimeout = 90 * time.Second

func runRoom(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("room", "create|list|show|join|accept|leave|regenerate")
	}
	switch args[0] {
	case "create":
		return roomCreate(ctx, e, args[1:])
	case "list":
		return roomList(ctx, e)
	case "show":
		return roomShow(ctx, e, args[1:])
	case "join":
		return roomJoin(ctx, e, args[1:])
	case "accept":
		return roomAccept(ctx, e, args[1:])
	case "leave":
		return roomLeave(ctx, e, args[1:])
	case "regenerate":
		return roomRegenerate(ctx, e, args[1:])
	default:
		return usageErrf("room", args[0], "create|list|show|join|accept|leave|regenerate")
	}
}

func roomCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("room create")
	name := fs.String("name", "", "room name shown in the peer list")
	maxPeers := fs.Int("max-peers", 0, "maximum peers (0 uses the daemon default)")
	ttl := fs.Int("ttl", 0, "pairing code lifetime in seconds (0 uses the daemon default)")
	profile := fs.String("profile", "", "game profile name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var resp protocol.RoomCreateResponse
	if err := client.Call(ctx, protocol.MethodRoomCreate, protocol.RoomCreateRequest{
		Name:              *name,
		MaxPeers:          *maxPeers,
		PairingTTLSeconds: *ttl,
		GameProfile:       *profile,
	}, &resp); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(resp)
	}

	// The code is the whole point of the call, so it is printed verbatim and
	// alone on its own line: everything around it is decoration and everything
	// after it is noise, so a user can pipe the whole block and take line one.
	fmt.Printf("Room   %s\n", resp.Room.RoomID)
	fmt.Printf("Name   %s\n", resp.Room.Name)
	fmt.Printf("Peers  %d of %d\n", resp.Room.PeerCount, resp.Room.MaxPeers)
	fmt.Printf("Expires %s\n", resp.ExpiresAt.Local().Format(time.RFC3339))
	fmt.Println()
	fmt.Printf("Pairing code (give this to your guest):\n%s\n", resp.PairingCode)
	fmt.Println()
	fmt.Printf("Or as a link:\n%s\n", resp.PairingURI)
	return nil
}

func roomList(ctx context.Context, e *env) error {
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var rooms []protocol.RoomSummary
	if err := client.Call(ctx, protocol.MethodRoomList, nil, &rooms); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(rooms)
	}
	if len(rooms) == 0 {
		fmt.Println("No rooms. Create one with: lanbazctl room create")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROOM\tNAME\tROLE\tPEERS\tPAIRING")
	for _, r := range rooms {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\n",
			r.RoomID, r.Name, roleOf(r.IsHost), r.PeerCount, r.MaxPeers, pairingOf(r))
	}
	return w.Flush()
}

func roomShow(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("room show", "<room-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var r protocol.RoomSummary
	if err := client.Call(ctx, protocol.MethodRoomGet,
		struct {
			RoomID string `json:"room_id"`
		}{RoomID: args[0]}, &r); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(r)
	}
	fmt.Printf("Room     %s (%s)\n", r.RoomID, roleOf(r.IsHost))
	fmt.Printf("Owner    %s\n", r.Owner)
	fmt.Printf("Peers    %d of %d\n", r.PeerCount, r.MaxPeers)
	fmt.Printf("Pairing  %s\n", pairingOf(r))
	if len(r.Peers) == 0 {
		fmt.Println("\nNo peers have joined yet.")
		return nil
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PEER\tNAME\tSTATE\tLINK\tRTT\tLOSS")
	for _, p := range r.Peers {
		name := p.DisplayName
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1fms\t%.0f%%\n",
			p.PeerID, name, p.State, p.LinkKind, p.RTTMillis, p.PacketLoss*100)
	}
	return w.Flush()
}

func roomJoin(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("room join")
	roomID := fs.String("room", "", "paste the guest's answer code here instead of a room id")
	name := fs.String("name", "", "display name other players see")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var code string
	switch {
	case *roomID != "":
		code = *roomID
	case len(args) == 1:
		code = args[0]
	default:
		return usageErr("room join", "<pairing-code> | --room <answer-code>")
	}

	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var resp protocol.RoomJoinResponse
	if err := client.Call(ctx, protocol.MethodRoomJoin, protocol.RoomJoinRequest{
		PairingCode: code,
		DisplayName: *name,
	}, &resp); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(resp)
	}
	fmt.Printf("Joined room %s as %s\n", resp.Room.RoomID, resp.LocalPeer.PeerID)
	if resp.Pending && resp.AnswerCode != "" {
		fmt.Println()
		fmt.Println("Send this reply code back to the host so they can finish the connection:")
		fmt.Println(resp.AnswerCode)
		fmt.Println()
		fmt.Println("The link stays closed until the host applies it. This is expected.")
	}
	return nil
}

func roomAccept(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("room accept")
	roomID := fs.String("room", "", "room id (optional: the answer code names its own room)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var code string
	switch {
	case len(args) == 1:
		code = args[0]
	case len(args) == 2:
		// The room id is the optional trailing argument, so the answer code -
		// the long, distinctive thing - always comes first and can be pasted on
		// its own.
		code, *roomID = args[0], args[1]
	default:
		return usageErr("room accept", "<answer-code> [<room-id>]")
	}

	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Call(ctx, protocol.MethodRoomAccept, protocol.RoomAcceptRequest{
		RoomID:     *roomID,
		AnswerCode: code,
	}, nil); err != nil {
		return err
	}
	fmt.Println("Answer applied. The peer list will show the guest once the link opens.")
	return nil
}

func roomLeave(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("room leave", "<room-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Call(ctx, protocol.MethodRoomLeave, struct {
		RoomID string `json:"room_id"`
	}{RoomID: args[0]}, nil); err != nil {
		return err
	}
	fmt.Printf("Left %s\n", args[0])
	return nil
}

func roomRegenerate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("room regenerate")
	ttl := fs.Int("ttl", 0, "new code lifetime in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(args) == 0 {
		return usageErr("room regenerate", "<room-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var resp protocol.PairingResponse
	if err := client.Call(ctx, protocol.MethodRoomRegeneratePairing,
		protocol.RoomRegeneratePairingRequest{RoomID: args[0], TTLSeconds: *ttl}, &resp); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(resp)
	}
	fmt.Printf("New pairing code (expires %s):\n%s\n", resp.ExpiresAt.Local().Format(time.RFC3339), resp.PairingCode)
	return nil
}

// ------------------------------------------------------------------- peers --

func runPeer(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("peer", "list|show|ping|kick")
	}
	switch args[0] {
	case "list":
		return peerList(ctx, e, args[1:])
	case "show":
		return peerShow(ctx, e, args[1:])
	case "ping":
		return peerPing(ctx, e, args[1:])
	case "kick":
		return peerKick(ctx, e, args[1:])
	default:
		return usageErrf("peer", args[0], "list|show|ping|kick")
	}
}

func peerList(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("peer list", "<room-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var peers []protocol.PeerSummary
	if err := client.Call(ctx, protocol.MethodPeerList, struct {
		RoomID string `json:"room_id"`
	}{RoomID: args[0]}, &peers); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(peers)
	}
	if len(peers) == 0 {
		fmt.Println("No peers yet.")
		return nil
	}
	return printPeerTable(peers)
}

func peerShow(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("peer show", "<peer-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var p protocol.PeerSummary
	if err := client.Call(ctx, protocol.MethodPeerGet,
		protocol.PeerPingRequest{PeerID: protocol.PeerID(args[0])}, &p); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(p)
	}
	printPeerDetail(p)
	return nil
}

func peerPing(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("peer ping")
	count := fs.Int("count", 0, "number of probes (0 uses the daemon default)")
	timeout := fs.Int("timeout", 0, "overall timeout in milliseconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(args) == 0 {
		return usageErr("peer ping", "<peer-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var res protocol.PeerPingResult
	err = client.Call(ctx, protocol.MethodPeerPing, protocol.PeerPingRequest{
		PeerID:        protocol.PeerID(args[0]),
		Count:         *count,
		TimeoutMillis: *timeout,
	}, &res)
	if e.asJSON {
		// The partial result is printed even on failure: two of four probes
		// answered is more informative than an error with no numbers attached.
		if jerr := printJSON(res); jerr != nil {
			return jerr
		}
		return err
	}
	if res.Sent > 0 {
		fmt.Printf("sent %d, received %d (%.0f%% loss)\n", res.Sent, res.Received, res.Loss*100)
		fmt.Printf("rtt %.2fms (min %.2f, max %.2f), jitter %.2fms\n",
			res.RTTMillis, res.MinMillis, res.MaxMillis, res.JitterMs)
	}
	return err
}

func peerKick(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("peer kick")
	roomID := fs.String("room", "", "room id (optional: the peer id is unique per installation)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(args) == 0 {
		return usageErr("peer kick", "<peer-id>")
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Call(ctx, protocol.MethodPeerKick, struct {
		RoomID string          `json:"room_id"`
		PeerID protocol.PeerID `json:"peer_id"`
	}{RoomID: *roomID, PeerID: protocol.PeerID(args[0])}, nil); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", args[0])
	return nil
}

// ----------------------------------------------------------------- helpers --

// longRunning reports whether a command needs the widened timeout budget.
//
// Room and peer commands do: room.create gathers ICE candidates, which takes
// seconds on a good connection and the full timeout on a bad one, and neither is
// something a shorter budget would make more correct.
func longRunning(name string) bool {
	return name == "room" || name == "peer"
}

func printPeerTable(peers []protocol.PeerSummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PEER\tNAME\tSTATE\tLINK\tRTT\tJITTER\tLOSS")
	for _, p := range peers {
		name := p.DisplayName
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1fms\t%.1fms\t%.0f%%\n",
			p.PeerID, name, p.State, p.LinkKind, p.RTTMillis, p.JitterMs, p.PacketLoss*100)
	}
	return w.Flush()
}

func printPeerDetail(p protocol.PeerSummary) {
	name := p.DisplayName
	if name == "" {
		name = "-"
	}
	fmt.Printf("Peer        %s (%s)\n", p.PeerID, name)
	fmt.Printf("State       %s\n", p.State)
	fmt.Printf("Link        %s\n", p.LinkKind)
	fmt.Printf("Fingerprint %s\n", p.Fingerprint)
	fmt.Printf("Round trips %d\n", p.RoundTrips)
	fmt.Printf("RTT         %.2fms (jitter %.2fms, loss %.0f%%)\n",
		p.RTTMillis, p.JitterMs, p.PacketLoss*100)
	fmt.Printf("Traffic     %d bytes sent, %d received\n", p.BytesSent, p.BytesRecv)
	fmt.Printf("Since       %s\n", p.Since.Local().Format(time.RFC3339))
}

func roleOf(isHost bool) string {
	if isHost {
		return "host"
	}
	return "guest"
}

func pairingOf(r protocol.RoomSummary) string {
	switch {
	case r.PairingSpent:
		return "used"
	case r.PairingExpiresAt.IsZero():
		return "none"
	case r.PairingExpiresAt.Before(time.Now()):
		return "expired"
	default:
		return "valid until " + r.PairingExpiresAt.Local().Format("15:04:05")
	}
}

// usageErr and usageErrf build a BAD_REQUEST error rather than printing usage
// and returning an exit code, so the same message reaches a script as JSON and a
// human as text.
func usageErr(verb, want string) error {
	return protocol.NewErrorf(protocol.CodeBadRequest,
		"usage: lanbazctl %s %s", verb, want)
}

func usageErrf(verb, got, want string) error {
	return protocol.NewErrorf(protocol.CodeBadRequest,
		"lanbazctl: unknown %s subcommand %q (want %s)", verb, got, want)
}

// sortedStrings is used by the tests that assert the command surface.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

var _ = strings.TrimSpace
