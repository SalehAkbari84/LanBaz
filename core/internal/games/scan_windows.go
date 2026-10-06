//go:build windows

package games

import (
	"encoding/binary"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi           = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcp = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdp = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet                   = 2
	tcpTableOwnerPIDListener = 3
	udpTableOwnerPID         = 1
	errInsufficientBuffer    = 122
)

// scanProcesses lists processes and attaches the sockets each one owns.
func scanProcesses(wantPath func(exe string) bool) ([]Process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	byPID := map[uint32]*Process{}
	var order []uint32
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := normExe(windows.UTF16ToString(e.ExeFile[:]))
		if name == "" || e.ProcessID == 0 {
			continue
		}
		proc := &Process{PID: e.ProcessID, Exe: name}
		if wantPath != nil && wantPath(name) {
			proc.Path = imagePath(e.ProcessID)
		}
		byPID[e.ProcessID] = proc
		order = append(order, e.ProcessID)
	}

	for _, s := range tcpListeners() {
		if p, ok := byPID[s.pid]; ok {
			p.Sockets = append(p.Sockets, s.sock)
		}
	}
	for _, s := range udpSockets() {
		if p, ok := byPID[s.pid]; ok {
			p.Sockets = append(p.Sockets, s.sock)
		}
	}
	out := make([]Process, 0, len(order))
	for _, pid := range order {
		out = append(out, *byPID[pid])
	}
	return out, nil
}

type ownedSocket struct {
	pid  uint32
	sock Socket
}

// table calls a GetExtended*Table function with a growing buffer.
func table(proc *windows.LazyProc, class uintptr) []byte {
	if proc.Find() != nil {
		return nil
	}
	size := uint32(16 * 1024)
	for i := 0; i < 4; i++ {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, class, 0)
		if r == 0 {
			return buf[:size]
		}
		if r != errInsufficientBuffer {
			return nil
		}
		size += 4096
	}
	return nil
}

// tcpListeners parses MIB_TCPTABLE_OWNER_PID (listeners only): a uint32
// count, then rows of six uint32s: state, local addr, local port, remote addr,
// remote port, owning pid.
func tcpListeners() []ownedSocket {
	buf := table(procGetExtendedTcp, tcpTableOwnerPIDListener)
	if len(buf) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(buf))
	const row = 24
	var out []ownedSocket
	for i := 0; i < n && 4+(i+1)*row <= len(buf); i++ {
		r := buf[4+i*row:]
		out = append(out, ownedSocket{
			pid: binary.LittleEndian.Uint32(r[20:]),
			sock: Socket{
				Proto: "tcp",
				Addr:  netip.AddrFrom4([4]byte{r[4], r[5], r[6], r[7]}),
				Port:  int(binary.BigEndian.Uint16(r[8:10])),
			},
		})
	}
	return out
}

// udpSockets parses MIB_UDPTABLE_OWNER_PID: a uint32 count, then rows of
// three uint32s: local addr, local port, owning pid.
func udpSockets() []ownedSocket {
	buf := table(procGetExtendedUdp, udpTableOwnerPID)
	if len(buf) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(buf))
	const row = 12
	var out []ownedSocket
	for i := 0; i < n && 4+(i+1)*row <= len(buf); i++ {
		r := buf[4+i*row:]
		out = append(out, ownedSocket{
			pid: binary.LittleEndian.Uint32(r[8:]),
			sock: Socket{
				Proto: "udp",
				Addr:  netip.AddrFrom4([4]byte{r[0], r[1], r[2], r[3]}),
				Port:  int(binary.BigEndian.Uint16(r[4:6])),
			},
		})
	}
	return out
}

// imagePath returns a process's full executable path, or "".
func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
