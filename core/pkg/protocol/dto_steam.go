package protocol

import "time"

// Steam during an outage: Steam's own offline mode, made one click.
const (
	// MethodSteamStatus reports whether this PC can play Steam games offline,
	// and lists the installed games.
	MethodSteamStatus = "steam.status"
	// MethodSteamSetOffline switches Steam's own offline-mode setting.
	MethodSteamSetOffline = "steam.set_offline"
	// MethodSteamSetExe remembers which program "Launch directly" starts.
	MethodSteamSetExe = "steam.set_exe"
)

// SteamAccount is the account Steam signs in with next.
type SteamAccount struct {
	Name string `json:"name"`
	// Remembered: Steam can start with no connection. Offline mode needs it.
	Remembered bool      `json:"remembered"`
	Offline    bool      `json:"offline"`
	LastLogin  time.Time `json:"last_login,omitempty"`
}

// SteamGame is one installed game.
type SteamGame struct {
	AppID      string `json:"app_id"`
	Name       string `json:"name"`
	InstallDir string `json:"install_dir"`
	// The LanBaz profile it matched, when one did.
	ProfileID    string   `json:"profile_id,omitempty"`
	ProfileName  string   `json:"profile_name,omitempty"`
	NeedsL2      bool     `json:"needs_l2,omitempty"`
	Availability []string `json:"availability,omitempty"`
	// Exe is what "Launch directly" starts: the player's choice, or the
	// program the profile names when it was found in the folder.
	Exe       string `json:"exe,omitempty"`
	ExeChosen bool   `json:"exe_chosen,omitempty"`
}

// SteamStatus is the outage readiness of this PC.
type SteamStatus struct {
	Installed bool          `json:"installed"`
	Path      string        `json:"path,omitempty"`
	Running   bool          `json:"running"`
	Account   *SteamAccount `json:"account,omitempty"`
	Games     []SteamGame   `json:"games"`
}

// SteamSetOfflineRequest turns offline mode on or off.
type SteamSetOfflineRequest struct {
	On bool `json:"on"`
}

// SteamSetExeRequest sets (or, with an empty path, clears) a game's program.
type SteamSetExeRequest struct {
	AppID string `json:"app_id"`
	Path  string `json:"path"`
}
