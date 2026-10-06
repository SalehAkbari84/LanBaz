package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The network verbs.
//
// They exist for the same reason the room verbs do: everything a user needs in
// order to answer "why can my game not see my friend" has to be reachable
// without the desktop shell. That question is almost always answered by an
// address and a counter, and both are here.

func runNetwork(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErr("network", "status|routes|interface|peers")
	}
	switch args[0] {
	case "status":
		return networkStatus(ctx, e, args[1:])
	case "routes":
		return networkRoutes(ctx, e, args[1:])
	case "interface":
		return networkInterface(ctx, e, args[1:])
	case "peers":
		return networkPeers(ctx, e, args[1:])
	default:
		return usageErrf("network", args[0], "status|routes|interface|peers")
	}
}

// roomFlag parses the optional --room selector every network verb takes.
func roomFlag(args []string) (*string, []string, error) {
	fs := newFlagSet("network")
	room := fs.String("room", "", "room id (default: the oldest room)")
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	return room, fs.Args(), nil
}

func networkStatus(ctx context.Context, e *env, args []string) error {
	room, _, err := roomFlag(args)
	if err != nil {
		return err
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var st protocol.NetworkStatus
	if err := client.Call(ctx, protocol.MethodNetworkStatus,
		protocolNetworkRequest(*room), &st); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(st)
	}

	fmt.Printf("Room     %s\n", orDash(st.RoomID))
	fmt.Printf("State    %s\n", orDash(st.State))
	fmt.Printf("Adapter  %s\n", orDash(st.Adapter))
	fmt.Printf("Interface %s\n", orDash(st.Interface))
	fmt.Printf("Subnet   %s\n", orDash(st.Subnet))
	fmt.Printf("Address  %s\n", orDash(st.LocalAddress))
	if st.MTU > 0 {
		fmt.Printf("MTU      %d\n", st.MTU)
	}
	fmt.Printf("Host     %v\n", st.IsHost)
	if !st.StartedAt.IsZero() {
		fmt.Printf("Started  %s\n", st.StartedAt.Local().Format(time.RFC3339))
	}
	// The note is printed before the counters and is never suppressed: it is the
	// one line that explains why a LAN is not moving, and hiding it behind a
	// verbosity flag would defeat the purpose of the command.
	if st.Note != "" {
		fmt.Println()
		fmt.Printf("Note: %s\n", st.Note)
	}
	if m := st.Metrics; m != (protocol.NetworkMetrics{}) {
		fmt.Println()
		fmt.Println("Packets")
		w := tabwriter.NewWriter(stdoutWriter(), 0, 4, 2, ' ', 0)
		metricRow(w, "delivered", m.Delivered)
		metricRow(w, "forwarded", m.Forwarded)
		metricRow(w, "relayed", m.Relayed)
		metricRow(w, "dropped", m.Dropped)
		metricRow(w, "no route", m.NoRoute)
		metricRow(w, "rate limited", m.RateLimited)
		metricRow(w, "malformed", m.Malformed)
		metricRow(w, "ignored (non-IPv4)", m.Ignored)
		metricRow(w, "oversized", m.Oversized)
		metricRow(w, "expired", m.Expired)
		// Spoofed is called out separately because it should always be zero. A
		// non-zero value means a peer tried to speak as somebody else, which is
		// not a statistic but an incident.
		metricRow(w, "spoofed", m.Spoofed)
		metricRow(w, "bytes in", m.BytesDelivered)
		metricRow(w, "bytes out", m.BytesForwarded)
		_ = w.Flush()
	}
	return nil
}

func networkRoutes(ctx context.Context, e *env, args []string) error {
	room, _, err := roomFlag(args)
	if err != nil {
		return err
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var routes []protocol.RouteEntry
	if err := client.Call(ctx, protocol.MethodNetworkRoutes,
		protocolNetworkRequest(*room), &routes); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(routes)
	}
	if len(routes) == 0 {
		fmt.Println("No routes are installed.")
		return nil
	}
	w := tabwriter.NewWriter(stdoutWriter(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DESTINATION\tNEXT HOP\tPEER\tNOTE")
	for _, r := range routes {
		note := r.Note
		if r.Peer != "" {
			note = r.Peer
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			r.Destination, orDash(r.NextHop), orDash(r.Peer), orDash(note))
	}
	return w.Flush()
}

func networkInterface(ctx context.Context, e *env, args []string) error {
	room, _, err := roomFlag(args)
	if err != nil {
		return err
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var iface protocol.NetworkInterface
	if err := client.Call(ctx, protocol.MethodNetworkInterface,
		protocolNetworkRequest(*room), &iface); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(iface)
	}
	// This verb exists to answer one question - what do I type into the game -
	// so the address is the first line rather than buried under the adapter name.
	fmt.Printf("Address    %s\n", orDash(iface.Address))
	fmt.Printf("Interface  %s\n", orDash(iface.Interface))
	fmt.Printf("Subnet     %s\n", orDash(iface.Subnet))
	fmt.Printf("Adapter    %s\n", orDash(iface.Adapter))
	fmt.Printf("State      %s\n", orDash(iface.State))
	fmt.Printf("MTU        %d\n", iface.MTU)
	fmt.Printf("Broadcast  %v (relayed by LanBaz when false)\n", iface.Broadcast)
	fmt.Printf("Multicast  %v (relayed by LanBaz when false)\n", iface.Multicast)
	return nil
}

// networkPeers prints the address book, which is the other half of "what do I
// type into the game" - the other machine's address, not this one's.
func networkPeers(ctx context.Context, e *env, args []string) error {
	room, _, err := roomFlag(args)
	if err != nil {
		return err
	}
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var st protocol.NetworkStatus
	if err := client.Call(ctx, protocol.MethodNetworkStatus,
		protocolNetworkRequest(*room), &st); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(st.Peers)
	}
	if len(st.Peers) == 0 {
		fmt.Println("No addresses are assigned in this room.")
		return nil
	}
	w := tabwriter.NewWriter(stdoutWriter(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ADDRESS\tROLE\tNAME\tPEER ID")
	for _, p := range st.Peers {
		name := p.DisplayName
		if p.Local {
			name = p.DisplayName + " (you)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Address, p.Role, orDash(name), orDash(p.PeerID))
	}
	return w.Flush()
}

// protocolNetworkRequest builds the request payload the three methods share.
func protocolNetworkRequest(roomID string) map[string]string {
	if roomID == "" {
		return map[string]string{}
	}
	return map[string]string{"room_id": roomID}
}

// metricRow prints one counter, skipping the ones that are still zero so the
// output stays readable. Dropped, spoofed and the byte totals are always shown
// because a zero is meaningful for those and silence would be ambiguous.
func metricRow(w *tabwriter.Writer, name string, v uint64) {
	if v == 0 {
		return
	}
	fmt.Fprintf(w, "  %s\t%d\n", name, v)
}

// orDash renders an empty field as a dash so columns stay aligned.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// stdoutWriter exists so the tabwriters all write through one place.
func stdoutWriter() *os.File { return os.Stdout }
