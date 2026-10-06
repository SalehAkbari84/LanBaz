# Graph Report - lanbaz  (2026-10-05)

## Corpus Check
- 319 files · ~200,797 words
- Verdict: corpus is large enough that graph structure adds value.
- Unclassified: 9 file(s) not represented in the graph (top: (none) 3, .ico 1, .manifest 1)

## Summary
- 2559 nodes · 7235 edges · 115 communities (99 shown, 16 thin omitted)
- Extraction: 94% EXTRACTED · 6% INFERRED · 0% AMBIGUOUS · INFERRED: 438 edges (avg confidence: 0.85)
- Token cost: 118,917 input · 0 output

## Community Hubs (Navigation)
- Shell Daemon Supervisor (Rust)
- Overlay Hotkeys (Rust)
- UI Components & Pages
- Daemon Wiring & Settings
- lanbazctl CLI
- Identity & Peer IDs
- Peer Manager
- Cross-Cutting Daemon Files
- Frontend Dependencies
- Game Detection
- Invite & TAP Notices UI
- Name Resolution (.local)
- Protocol Client & Envelope
- DaemonClient Methods
- App Shell & Navigation
- API Types (TS)
- Scripts & Integration Tests
- Single Instance (Win32)
- Packet Router Actions
- Tauri Config
- API Server Tests
- Wintun Signatures & Mesh
- Daemon Entry & Logging
- Doctor: Local Layers
- Transport Interface & L2 Nodes
- Pairing Code Format
- Room Manager Wiring
- Router & Broadcast Limiter
- IPAM Allocator
- Pairing & Peer Tests
- WebRTC Link State
- Doctor: Internet Layer
- Room State (App/Mesh/Presence)
- Process & Egress (Windows)
- API Room Handlers
- Room Manager
- API Handler Registry
- Daemon Config
- Network Service
- Room Handlers & Recovery
- i18n (fa/en)
- Network Backend Factories
- L2 Ethernet Switch
- Room Network & Pending Joins
- Routes & Host Addresses
- Room/Peer DTOs
- Tray & Sidecar Processes
- TS Config
- Doctor: Two-PC Test
- Network Service Tests
- IPAM Tests
- Room Chat Messages
- WebRTC Transport Core
- Room Integration Tests
- Room Network Tests
- API Connection Loop
- IPv4 Packet Parsing
- Wintun Adapter Binding
- Network Status DTOs
- Daemon Lifecycle Tests
- Docs: Overlay & Control API
- Rooms Store (UI)
- TAP Adapter Discovery
- SDP Compaction
- WebRTC Transport Tests
- Logging Sinks
- Connection Check & TURN Probe
- Log Redaction Tests
- NAT Lab Tests
- In-Memory Adapter
- Docs: Layered Architecture
- Slog Multi-Handler
- NAT Type Check
- Wintun Firewall & Cleanup
- State File
- Docs: Adapters & IPAM
- Frontend Entry
- Adapter & Transport Factories
- TAP Adapter Ops
- TS Node Config
- Wintun Adapter Interface
- VPN Bypass & NAT Keepalive
- API Rate Limiter
- Ping Telemetry
- WebRTC Construction
- Docs: Install & Wintun Bugs
- Wintun Probe Tool
- API Network Handlers
- Log Level Control
- Wintun Device IDs
- Peer State Machine
- Acceptance Phase 2
- API Method Catalog
- Acceptance Phase 1
- Tauri Capabilities
- Docs: Room Topology & Relay
- Wintun Header
- Egress (non-Windows)
- Docs: Desktop Shell
- Acceptance Script
- L2 Switch Tests
- Redactor
- Small: build-installer.sh script
- Small: build-sidecar.sh
- Small: dev.sh script
- Small: github.com/lanbaz/lanbaz
- Small: lanbaz

## God Nodes (most connected - your core abstractions)
1. `testingT` - 315 edges
2. `PeerID` - 106 edges
3. `NewErrorf()` - 96 edges
4. `NewError()` - 82 edges
5. `Manager` - 61 edges
6. `Manager` - 49 edges
7. `Service` - 47 edges
8. `useT()` - 46 edges
9. `DaemonClient` - 46 edges
10. `Room` - 45 edges

## Surprising Connections (you probably didn't know these)
- `wintun signature_test.go` --semantically_similar_to--> `wtprobe diagnostic tool`  [INFERRED] [semantically similar]
  docs/install.md → third_party/wintun/README.md
- `Room server (optional self-hosted rendezvous)` --semantically_similar_to--> `TURN relay (user-configured, e.g. coturn)`  [INFERRED] [semantically similar]
  roomserver/README.md → docs/play-with-a-friend.md
- `TestJoinURIFillsTheTemplate()` --references--> `testingT`  [EXTRACTED]
  profiles/profiles_test.go → core/internal/network/memory/memory.go
- `LanBaz (LAN gaming over the internet)` --references--> `Layered architecture (Game -> Virtual LAN -> Router -> Peer Manager -> Transport)`  [EXTRACTED]
  README.md → docs/architecture.md
- `App runs elevated (administrator)` --rationale_for--> `Wintun driver 0.14.1 (vendored, SHA-256 pinned)`  [INFERRED]
  docs/security.md → third_party/wintun/README.md

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Packet path: adapter -> Service -> Router -> Transport** — docs_networking_network_adapter, docs_networking_network_service, docs_networking_router, docs_architecture_transport_interface, docs_architecture_webrtc_transport [EXTRACTED 1.00]
- **Serverless pairing exchange (offer code -> answer code -> accept)** — docs_pairing_pairing_code, docs_pairing_hmac_sig, docs_protocol_room_methods, docs_play_with_a_friend_invites, docs_security_ed25519_identity [EXTRACTED 1.00]
- **Control-plane hardening (loopback, token hello, state file, redaction)** — docs_protocol_control_api, docs_protocol_hello_handshake, docs_protocol_daemon_json, docs_security_redactinghandler, docs_security_threat_model [INFERRED 0.85]

## Communities (115 total, 16 thin omitted)

### Community 0 - "Shell Daemon Supervisor (Rust)"
Cohesion: 0.06
Nodes (65): daemon_endpoint(), daemon_state_dir(), DaemonEndpoint, DaemonState, DaemonStateDir, endpoint_blocking(), graceful_shutdown(), is_running() (+57 more)

### Community 1 - "Overlay Hotkeys (Rust)"
Cohesion: 0.05
Nodes (61): CHAT_HOTKEY_LABEL, Hotkey, HOTKEY_LABEL, F, Mutex, Option, String, run() (+53 more)

### Community 2 - "UI Components & Pages"
Cohesion: 0.10
Nodes (50): Avatar(), Banner(), Card(), CopyButton(), EmptyState(), hashHue(), initialsOf(), PageHeader() (+42 more)

### Community 3 - "Daemon Wiring & Settings"
Cohesion: 0.07
Nodes (35): PlatformString(), Daemon, hostname(), New(), relayServers(), checkURL(), clone(), EffectiveSTUN() (+27 more)

### Community 4 - "lanbazctl CLI"
Cohesion: 0.12
Nodes (53): commands(), daemonShutdown(), daemonStatus(), daemonVersion(), dial(), errorCode(), exitCodeFor(), main() (+45 more)

### Community 5 - "Identity & Peer IDs"
Cohesion: 0.06
Nodes (42): DeriveID(), deriveID(), Fingerprint(), Generate(), Identity, PeerID, PeerID, Load() (+34 more)

### Community 6 - "Peer Manager"
Cohesion: 0.10
Nodes (9): base64RawURL(), Event, Manager, Peer, ControlReceiver, ControlTransport, StateReporter, sync.RWMutex (+1 more)

### Community 7 - "Cross-Cutting Daemon Files"
Cohesion: 0.15
Nodes (22): CleanupStale(), Adapter, Options, New(), go_pkg_context, go_pkg_github_com_lanbaz_lanbaz_core_internal_identity, go_pkg_github_com_lanbaz_lanbaz_core_internal_network, go_pkg_github_com_lanbaz_lanbaz_core_internal_network_ipam (+14 more)

### Community 8 - "Frontend Dependencies"
Cohesion: 0.04
Nodes (45): dependencies, @fontsource/inter, @fontsource/vazirmatn, framer-motion, lucide-react, react, react-dom, @tauri-apps/api (+37 more)

### Community 9 - "Game Detection"
Cohesion: 0.08
Nodes (34): Detector, lanbazBound(), New(), normExe(), reachablePorts(), sortedPorts(), systemProcess(), fake() (+26 more)

### Community 10 - "Invite & TAP Notices UI"
Cohesion: 0.09
Nodes (38): IncomingCode(), looksLikeCode(), Source, TapDriverNotice(), Modal(), translate(), ConnectionState, daemonEndpoint (+30 more)

### Community 11 - "Name Resolution (.local)"
Cohesion: 0.07
Nodes (14): answerNameQuery(), buildUDP(), encodeName(), NameLabel(), readName(), dnsQuery(), TestAnswersMDNSAndLLMNRForPlayers(), TestNameLabel() (+6 more)

### Community 12 - "Protocol Client & Envelope"
Cohesion: 0.09
Nodes (27): Dial(), Client, Decode(), Message, marshalPayload(), NewErrorResponse(), NewEvent(), NewEventID() (+19 more)

### Community 13 - "DaemonClient Methods"
Cohesion: 0.10
Nodes (4): DaemonClient, describe(), PeerSummary, RoomEvent

### Community 14 - "App Shell & Navigation"
Cohesion: 0.11
Nodes (34): App(), ITEMS, Sidebar(), Toaster(), useDocumentDirection(), ChatPage(), ChatPanel(), OverlayPage() (+26 more)

### Community 15 - "API Types (TS)"
Cohesion: 0.07
Nodes (35): ApiError, ApiEvent, ApiResponse, DaemonClientOptions, DaemonError, Endpoint, MessageKind, Pending (+27 more)

### Community 16 - "Scripts & Integration Tests"
Cohesion: 0.07
Nodes (31): base64, json, os, pathlib, blend(), dist_to_point(), dist_to_segment(), ico_bytes() (+23 more)

### Community 17 - "Single Instance (Win32)"
Cohesion: 0.10
Nodes (35): acquire(), CloseHandle(), CreateEventW(), CreateMutexW(), ERROR_ALREADY_EXISTS, EVENT_NAME, GetLastError(), listen() (+27 more)

### Community 18 - "Packet Router Actions"
Cohesion: 0.09
Nodes (11): ActionDropReason(), Packet, net/netip.Addr, sync/atomic.Uint64, Action, broadcastLimiter, bucket, Decision (+3 more)

### Community 19 - "Tauri Config"
Cohesion: 0.05
Nodes (36): app, security, windows, withGlobalTauri, build, beforeBuildCommand, beforeDevCommand, devUrl (+28 more)

### Community 20 - "API Server Tests"
Cohesion: 0.12
Nodes (35): contains(), dial(), errorsAs(), Options, Server, newTestServer(), newTestServerBefore(), TestBadTokenIsRejected() (+27 more)

### Community 21 - "Wintun Signatures & Mesh"
Cohesion: 0.09
Nodes (21): parseBinding(), TestEveryExportIsAccountedFor(), TestWintunCallsMatchUpstreamSignatures(), Room, isLive(), meshLink(), pairKey(), Signalling (+13 more)

### Community 22 - "Daemon Entry & Logging"
Cohesion: 0.08
Nodes (24): main(), mtuFlag(), portFlag(), run(), equalFold(), maskTokens(), matchSecretKey(), redactAttr() (+16 more)

### Community 23 - "Doctor: Local Layers"
Cohesion: 0.11
Nodes (20): main(), newReport(), runAll(), layerLocalWebRTC(), layerTAP(), isAdmin(), layerSystem(), ps() (+12 more)

### Community 24 - "Transport Interface & L2 Nodes"
Cohesion: 0.11
Nodes (13): transportID(), newL2Node(), udpPacketRaw(), Config, Packet, PeerID, TransportStats, sync.Mutex (+5 more)

### Community 25 - "Pairing Code Format"
Cohesion: 0.11
Nodes (24): Decode(), decodePayload(), decodeSecret(), encodePayload(), group(), inflate(), New(), normalize() (+16 more)

### Community 26 - "Room Manager Wiring"
Cohesion: 0.11
Nodes (18): GameService, TestAsAPIErrorMapsSentinels(), asAPIError(), NewNonce(), NewSecret(), NewManager(), CreateRoom(), decodeSignal() (+10 more)

### Community 27 - "Router & Broadcast Limiter"
Cohesion: 0.18
Nodes (31): newBroadcastLimiter(), buildIPv4(), NewRouter(), addPeer(), addr(), ipamHostAddress(), TestActionStrings(), TestBroadcastLimiterBoundsFanOut() (+23 more)

### Community 28 - "IPAM Allocator"
Cohesion: 0.11
Nodes (12): Describe(), Allocator, hostAt(), sortLeases(), SubnetBroadcast(), TestDescribe(), go_pkg_hash_fnv, net/netip.Prefix (+4 more)

### Community 29 - "Pairing & Peer Tests"
Cohesion: 0.14
Nodes (30): newTestCode(), TestDecodeRejectsGarbage(), TestDecodeRejectsOversizedInput(), TestDecodeToleratesHumanEntry(), TestRoundTrip(), TestSignRejectsShortSecret(), TestStringOmitsSecretAndSignal(), TestTwoCodesDifferWithinTheSameSecond() (+22 more)

### Community 30 - "WebRTC Link State"
Cohesion: 0.12
Nodes (16): boolPtr(), Transport, webrtc.PeerConnectionState, stateIndex(), uint16Ptr(), classifyConnectionState(), fmtPeer(), webrtc.PeerConnectionState (+8 more)

### Community 31 - "Doctor: Internet Layer"
Cohesion: 0.10
Nodes (22): iceGather(), layerInternet(), netAddrPort(), portPattern(), TestCandidateFilterDropsAddressesNoFriendCanReach(), usableCandidateIP(), go_pkg_github_com_lanbaz_lanbaz_core_internal_api, go_pkg_github_com_lanbaz_lanbaz_core_internal_games (+14 more)

### Community 32 - "Room State (App/Mesh/Presence)"
Cohesion: 0.10
Nodes (18): Daemon, Daemon, sameGame(), cleanChat(), newAppState(), newMeshState(), Room, newPresenceState() (+10 more)

### Community 33 - "Process & Egress (Windows)"
Cohesion: 0.09
Nodes (20): currentUserSIDString(), TestIdentityFileACLIsRestricted(), Enabled(), findPhysical(), htonl(), indexForIP(), ListenUDP(), Physical() (+12 more)

### Community 34 - "API Room Handlers"
Cohesion: 0.22
Nodes (7): RoomService, SettingsService, decode(), Server, decodeOptional(), context.Context, encoding/json.RawMessage

### Community 35 - "Room Manager"
Cohesion: 0.17
Nodes (6): Options, Manager, ptr(), LocalGame, Room, T

### Community 36 - "API Handler Registry"
Cohesion: 0.10
Nodes (13): Handler, Options, registry, TestServerRejectsEmptyTokenAndLogger(), newRegistry(), Server, isLoopbackRequest(), New() (+5 more)

### Community 37 - "Daemon Config"
Cohesion: 0.11
Nodes (23): applyEnv(), Default(), DefaultStateDir(), Config, Load(), TestDefaultsAreValid(), TestDefaultStateDirIsPerUser(), TestEnvOverridesFile() (+15 more)

### Community 38 - "Network Service"
Cohesion: 0.17
Nodes (6): clone(), ctx(), Service, Packet, isNameQuery(), State

### Community 39 - "Room Handlers & Recovery"
Cohesion: 0.14
Nodes (13): go_pkg_crypto_rand, go_pkg_crypto_subtle, go_pkg_encoding_base64, go_pkg_encoding_hex, go_pkg_encoding_json, go_pkg_fmt, go_pkg_github_com_gorilla_websocket, go_pkg_github_com_lanbaz_lanbaz_core_internal_pairing (+5 more)

### Community 40 - "i18n (fa/en)"
Cohesion: 0.12
Nodes (20): en, Messages, fa, DICTS, isRtl(), Key, General(), NetworkTab() (+12 more)

### Community 41 - "Network Backend Factories"
Cohesion: 0.13
Nodes (19): adapterName(), Daemon, memoryNetworkFactory(), tapNetworkFactory(), waitStaleCleanup(), wintunNetworkFactory(), Daemon, NewSwitch() (+11 more)

### Community 42 - "L2 Ethernet Switch"
Cohesion: 0.17
Nodes (8): frameMACs(), Switch, macKey(), shortID(), context.CancelFunc, sync/atomic.Value, MAC, macEntry

### Community 43 - "Room Network & Pending Joins"
Cohesion: 0.11
Nodes (4): VirtualNetwork, svcPeers(), Room, pendingJoin

### Community 44 - "Routes & Host Addresses"
Cohesion: 0.13
Nodes (11): HostAddr(), Route, HostAddressOf(), TestHostAddressHelpers(), routeLess(), sortRoutes(), joinNotes(), PeerStatus (+3 more)

### Community 45 - "Room/Peer DTOs"
Cohesion: 0.13
Nodes (17): millis(), Notice, PeerID, PairingResponse, PeerEvent, PeerSummary, RoomAcceptRequest, RoomCreateRequest (+9 more)

### Community 46 - "Tray & Sidecar Processes"
Cohesion: 0.13
Nodes (19): build_tray(), DaemonArgs, DaemonProcesses, kill_sidecars(), AppHandle, Arc, Mutex, Self (+11 more)

### Community 47 - "TS Config"
Cohesion: 0.09
Nodes (22): compilerOptions, allowImportingTsExtensions, exactOptionalPropertyTypes, forceConsistentCasingInFileNames, isolatedModules, jsx, lib, module (+14 more)

### Community 48 - "Doctor: Two-PC Test"
Cohesion: 0.18
Nodes (21): candidateLines(), decodeCode(), encodeCode(), getClipboard(), indent(), makeLocal(), pairs(), pairTable() (+13 more)

### Community 49 - "Network Service Tests"
Cohesion: 0.33
Nodes (22): New(), NewService(), join(), newBus(), newFakeTransport(), newNode(), quietLogger(), TestASpoofedPacketIsDroppedInFlight() (+14 more)

### Community 50 - "IPAM Tests"
Cohesion: 0.17
Nodes (21): HostAddress(), New(), TestAdoptRecordsTheSubnetsTheHostNamed(), TestAdoptRefusesASubnetAnotherLocalRoomUses(), TestAdoptRefusesASubnetOutsideThePool(), TestAdoptReportsASubnetThatIsNotTheDerivedOne(), TestAllocateHandsOutDistinctAddresses(), TestAllocateIsIdempotentForAPeer() (+13 more)

### Community 51 - "Room Chat Messages"
Cohesion: 0.18
Nodes (9): AppMessage, Room, newMessageID(), PeerID, ChatMessage, time.Time, appState, issued (+1 more)

### Community 52 - "WebRTC Transport Core"
Cohesion: 0.12
Nodes (6): ControlHandler, PeerID, StateEvent, Transport, webrtc.API, webrtc.Configuration

### Community 53 - "Room Integration Tests"
Cohesion: 0.16
Nodes (16): findPeers(), TestRoomClosedDuringNetworkSetupRemovesTheNetwork(), anyActive(), describe(), generateIdentity(), newSide(), quietLogger(), TestServerlessPairingConnectsAndMeasures() (+8 more)

### Community 54 - "Room Network Tests"
Cohesion: 0.21
Nodes (19): drainEvents(), TestAcceptRejectsAnAnswerFromAnotherRoom(), TestRoomRejectsAGuestThatCannotBeReached(), udpPacket(), waitFor(), waitForStatus(), TestClassicLANModeReachesTheGuest(), mdnsPacket() (+11 more)

### Community 55 - "API Connection Loop"
Cohesion: 0.19
Nodes (7): conn, subscriber, writeItem, newConn(), github.com/gorilla/websocket.Conn, sync/atomic.Bool, stopSpy

### Community 56 - "IPv4 Packet Parsing"
Cohesion: 0.18
Nodes (18): decrementTTL(), FormatProtocol(), headerChecksum(), ParseIPv4(), SubnetBroadcast(), binaryPutUint16(), TestBroadcastClassification(), TestDecrementTTLExpiresTheLastHop() (+10 more)

### Community 57 - "Wintun Adapter Binding"
Cohesion: 0.18
Nodes (10): TestAlreadyExistsMatchesTheDriverError(), alreadyExists(), call(), createError(), driverError(), driverPointer(), Adapter, lastError() (+2 more)

### Community 58 - "Network Status DTOs"
Cohesion: 0.13
Nodes (10): fakeNetwork, NetworkStatus, RouteEntry, networkAdapter, NetworkMetrics, NetworkPeer, NetworkInterface, NetworkMetrics (+2 more)

### Community 59 - "Daemon Lifecycle Tests"
Cohesion: 0.25
Nodes (18): bytesContain(), Daemon, newTestDaemon(), startDaemon(), TestContextCancellationStopsDaemon(), TestDaemonLifecycleAndStatus(), TestDaemonShutdownStopsAndCleansUp(), TestDaemonVersionMethod() (+10 more)

### Community 60 - "Docs: Overlay & Control API"
Cohesion: 0.14
Nodes (19): Ctrl+Alt+L global hotkey, In-game overlay (non-injected window), Overlay toast / panel modes, useRoomsStore, Loopback WebSocket control API, daemon.json state file, JSON envelope (id/type/version/payload), Stable error codes (+11 more)

### Community 61 - "Rooms Store (UI)"
Cohesion: 0.22
Nodes (17): daemonClient(), describe(), keepOnly(), loadNetwork(), normalizeRoom(), now(), refreshRoom(), RoomAction (+9 more)

### Community 62 - "TAP Adapter Discovery"
Cohesion: 0.20
Nodes (15): Probe(), allTap(), Available(), connectionName(), defaultAlias(), findAdapters(), Options, New() (+7 more)

### Community 63 - "SDP Compaction"
Cohesion: 0.19
Nodes (7): Description, PeerInfo, candidateSummary(), Transport, compactSDP(), stripCandidateExtension(), TestCompactSDPKeepsOneComponentAndNoExtensions()

### Community 64 - "WebRTC Transport Tests"
Cohesion: 0.22
Nodes (17): TestLateAnswerStillConnects(), TestPendingLinkGivesUp(), Transport, newTestTransport(), TestAcceptOfferRejectsEmptyDescription(), TestApplyAnswerRejectsUnknownPeer(), TestCloseUnknownPeerIsNotAnError(), testKey() (+9 more)

### Community 65 - "Logging Sinks"
Cohesion: 0.14
Nodes (16): New(), newMultiHandler(), stderrOr(), newTestLogger(), TestCloseIsIdempotent(), TestConsoleAndFileSinksBothReceive(), TestFileSinkIsCreatedAndRestricted(), TestLevelsFilterOutput() (+8 more)

### Community 66 - "Connection Check & TURN Probe"
Cohesion: 0.18
Nodes (13): msOf(), buildReport(), Daemon, TURNProbe, probeOne(), ProbeTURN(), DiagnoseReport, DiagnoseServer (+5 more)

### Community 67 - "Log Redaction Tests"
Cohesion: 0.18
Nodes (15): NewRedactingHandler(), NewRedactor(), TestConstantTimeEqual(), TestGenerateIDShape(), TestProcessAlive(), TestRedactingHandlerMasksMessagesAndAttrs(), TestRedactingHandlerWithAttrsAndGroup(), TestRedactingHandlerWithNilRedactorIsPassthrough() (+7 more)

### Community 68 - "NAT Lab Tests"
Cohesion: 0.19
Nodes (14): buildNATLab(), Transport, lateReply(), natTransport(), TestLateReplyConnectsThroughPortRestrictedNATs(), TestNATLabConnectsImmediately(), TestOldBehaviourFailsThroughPortRestrictedNATs(), go_pkg_github_com_pion_logging (+6 more)

### Community 69 - "In-Memory Adapter"
Cohesion: 0.19
Nodes (3): Adapter, MustConfigure(), Packet

### Community 70 - "Docs: Layered Architecture"
Cohesion: 0.15
Nodes (15): Layered architecture (Game -> Virtual LAN -> Router -> Peer Manager -> Transport), Signalling / ControlTransport / ControlReceiver / StateReporter, Peer state machine (core/internal/peer), transport.Transport interface, WebRTC transport (Pion, STUN/ICE), Code encoding (deflate + base32 without ILOU), HMAC-SHA256 code signature, Pairing code (LBZ-, offer/answer) (+7 more)

### Community 71 - "Slog Multi-Handler"
Cohesion: 0.24
Nodes (6): log/slog.Attr, log/slog.Handler, log/slog.Level, log/slog.Record, multiHandler, redactingHandler

### Community 72 - "NAT Type Check"
Cohesion: 0.30
Nodes (12): Check(), classify(), Report, resolve(), summarise(), res(), TestClassify(), TestLiveServers() (+4 more)

### Community 73 - "Wintun Firewall & Cleanup"
Cohesion: 0.30
Nodes (9): firewallRuleName(), firstLine(), Adapter, installFirewall(), removeFirewall(), CleanupStale(), removeDevice(), powershellEscape() (+1 more)

### Community 74 - "State File"
Cohesion: 0.29
Nodes (13): TestDescribeStateMasksToken(), TestReadStateFileErrors(), TestRemoveStateFile(), TestStateFileIsOwnerOnly(), TestStateFileIsValidJSON(), TestWriteAndReadStateFile(), TestWriteStateFileIsAtomicAndLeavesNoTempFiles(), describeState() (+5 more)

### Community 75 - "Docs: Adapters & IPAM"
Cohesion: 0.18
Nodes (14): Plan B: TAP-Windows6 behind network.Adapter, Known risk: Wintun is an L3 adapter, Game profile JSON schema, DiscoveryHandler interface (Minecraft/mDNS/SSDP), IPAM (10.200.<room>.0/24 via FNV-1a), In-memory adapter backend, network.Adapter interface (wintun / memory backends), network.Service (readAdapter/readTransport/writeAdapter) (+6 more)

### Community 76 - "Frontend Entry"
Cohesion: 0.15
Nodes (12): index.html React root mount, app_src_index, container, ref_fontsource_inter_400_css, ref_fontsource_inter_500_css, ref_fontsource_inter_600_css, ref_fontsource_inter_700_css, ref_fontsource_vazirmatn_400_css (+4 more)

### Community 77 - "Adapter & Transport Factories"
Cohesion: 0.18
Nodes (7): Adapter, Adapter, Transport, FactoryFunc, ServiceConfig, SwitchConfig, Factory

### Community 78 - "TAP Adapter Ops"
Cohesion: 0.23
Nodes (4): openTap(), ps(), psq(), Adapter

### Community 79 - "TS Node Config"
Cohesion: 0.18
Nodes (10): compilerOptions, allowSyntheticDefaultImports, composite, emitDeclarationOnly, module, moduleResolution, outDir, skipLibCheck (+2 more)

### Community 81 - "VPN Bypass & NAT Keepalive"
Cohesion: 0.27
Nodes (6): Transport, github.com/pion/transport/v5/stdnet.Net, github.com/pion/transport/v5.UDPConn, net.UDPAddr, bypassNet, keepNet

### Community 82 - "API Rate Limiter"
Cohesion: 0.31
Nodes (8): rateLimiter, newRateLimiter(), TestRateLimiterAllowsInitialBurst(), TestRateLimiterCapsAtBurst(), TestRateLimiterIsConcurrencySafe(), TestRateLimiterRefillsOverTime(), TestRateLimiterUnlimitedWhenRateIsZero(), TestRateLimiterWithMinimumBurst()

### Community 83 - "Ping Telemetry"
Cohesion: 0.33
Nodes (3): estimator, sample, Telemetry

### Community 84 - "WebRTC Construction"
Cohesion: 0.31
Nodes (9): NewBypassNet(), KeepAliveNet(), newKeepNet(), icePolicy(), New(), github.com/pion/transport/v5.Net, webrtc.ICEServer, webrtc.ICETransportPolicy (+1 more)

### Community 85 - "Docs: Install & Wintun Bugs"
Cohesion: 0.29
Nodes (10): MTU 1200, NSIS installer build, wintun signature_test.go, Wintun binding bugs (WintunStartSession arity, handles, DLL search), Invites (lanbaz:// link, .lanbaz file, clipboard), App runs elevated (administrator), Wintun Prebuilt Binaries License (WireGuard LLC), Wintun driver 0.14.1 (vendored, SHA-256 pinned) (+2 more)

### Community 86 - "Wintun Probe Tool"
Cohesion: 0.36
Nodes (9): call1(), call2(), call3(), lastErr(), loadDLL(), main(), proc2(), ptr() (+1 more)

### Community 87 - "API Network Handlers"
Cohesion: 0.42
Nodes (3): networkRequest, NetworkService, Server

### Community 88 - "Log Level Control"
Cohesion: 0.39
Nodes (5): NewLevelVar(), setLevel(), TestLevelVarCanChangeAtRuntime(), log/slog.LevelVar, LevelVar

### Community 89 - "Wintun Device IDs"
Cohesion: 0.29
Nodes (7): deviceID(), guidString(), TestDeviceIDMatchesWindowsFormat(), deriveGUID(), go_pkg_regexp, guid, guid

### Community 90 - "Peer State Machine"
Cohesion: 0.29
Nodes (3): rank(), stateFromIndex(), PeerState

### Community 91 - "Acceptance Phase 2"
Cohesion: 0.46
Nodes (6): fail(), json_get(), acceptance-phase2.sh script, start_daemon(), step(), wait_for_file()

### Community 92 - "API Method Catalog"
Cohesion: 0.33
Nodes (6): Events(), Methods(), assertUnique(), TestMethodsAndEventsAreUnique(), network.status / routes / interface methods, room.create / room.join / room.accept methods

### Community 93 - "Acceptance Phase 1"
Cohesion: 0.52
Nodes (5): fail(), acceptance-phase1.sh script, start_daemon(), step(), wait_for_file()

### Community 94 - "Tauri Capabilities"
Cohesion: 0.33
Nodes (5): description, identifier, permissions, $schema, windows

### Community 95 - "Docs: Room Topology & Relay"
Cohesion: 0.33
Nodes (6): Game detection (process + listening ports, 111 profiles), Broadcast/multicast relay through hub, Hub-and-spoke topology, Per-peer token bucket (100 pps, anti-amplification), Mesh links (guest-to-guest direct), Room messaging (chat, presence, mesh signalling)

### Community 96 - "Wintun Header"
Cohesion: 0.33
Nodes (5): ifdef, ipexport, windows, winsock2, ws2ipdef

### Community 98 - "Docs: Desktop Shell"
Cohesion: 0.40
Nodes (5): Daemon supervisor (probe 3s, backoff 2s-30s), Tauri desktop shell (Rust), Parent watch (no orphaned daemons, --managed-by-shell), Single instance (Win32 named mutex), spawn_sidecar (single sidecar spawn path)

### Community 99 - "Acceptance Script"
Cohesion: 0.60
Nodes (3): fail(), acceptance.sh script, step()

### Community 101 - "L2 Switch Tests"
Cohesion: 0.67
Nodes (4): frame(), l2join(), TestSwitchFloodsBroadcastAndLearnsUnicast(), TestSwitchRefusesAStolenMAC()

## Ambiguous Edges - Review These
- `App runs elevated (administrator)` → `NSIS installer build`  [AMBIGUOUS]
  docs/install.md · relation: conceptually_related_to

## Knowledge Gaps
- **191 isolated node(s):** `name`, `private`, `version`, `type`, `description` (+186 more)
  These have ≤1 connection - possible missing edges or undocumented components. (Counts symbols only; 467 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **16 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **What is the exact relationship between `App runs elevated (administrator)` and `NSIS installer build`?**
  _Edge tagged AMBIGUOUS (relation: conceptually_related_to) - confidence is low._
- **Why does `testingT` connect `Pairing & Peer Tests` to `Daemon Wiring & Settings`, `Identity & Peer IDs`, `Cross-Cutting Daemon Files`, `Game Detection`, `Name Resolution (.local)`, `Protocol Client & Envelope`, `API Server Tests`, `Wintun Signatures & Mesh`, `Daemon Entry & Logging`, `Transport Interface & L2 Nodes`, `Pairing Code Format`, `Room Manager Wiring`, `Router & Broadcast Limiter`, `IPAM Allocator`, `Doctor: Internet Layer`, `Process & Egress (Windows)`, `API Handler Registry`, `Daemon Config`, `Routes & Host Addresses`, `Network Service Tests`, `IPAM Tests`, `Room Integration Tests`, `Room Network Tests`, `IPv4 Packet Parsing`, `Wintun Adapter Binding`, `Daemon Lifecycle Tests`, `TAP Adapter Discovery`, `SDP Compaction`, `WebRTC Transport Tests`, `Logging Sinks`, `Log Redaction Tests`, `NAT Lab Tests`, `In-Memory Adapter`, `NAT Type Check`, `State File`, `API Rate Limiter`, `Log Level Control`, `Wintun Device IDs`, `API Method Catalog`, `L2 Switch Tests`?**
  _High betweenness centrality (0.228) - this node is a cross-community bridge._
- **Why does `TestMethodsAndEventsAreUnique()` connect `API Method Catalog` to `Protocol Client & Envelope`, `Pairing & Peer Tests`?**
  _High betweenness centrality (0.212) - this node is a cross-community bridge._
- **Why does `room.create / room.join / room.accept methods` connect `API Method Catalog` to `Docs: Layered Architecture`?**
  _High betweenness centrality (0.207) - this node is a cross-community bridge._
- **Are the 9 inferred relationships involving `NewErrorf()` (e.g. with `Dial()` and `Decode()`) actually correct?**
  _`NewErrorf()` has 9 INFERRED edges - model-reasoned connections that need verification._
- **Are the 9 inferred relationships involving `NewError()` (e.g. with `Dial()` and `Decode()`) actually correct?**
  _`NewError()` has 9 INFERRED edges - model-reasoned connections that need verification._
- **What connects `name`, `private`, `version` to the rest of the system?**
  _191 weakly-connected nodes found - possible documentation gaps or missing edges._