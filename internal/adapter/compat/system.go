package compat

import "net/http"

// Values reported by the system module. Branding is Jelee throughout; only
// the wire field names follow the upstream DTOs (see wire.go for sources).
const (
	// serverName is the friendly name clients show for this server.
	serverName = "Jelee"
	// productName is the product reported in system information and returned
	// by the ping route.
	productName = "Jelee Server"
	// protocolVersion is the upstream release line whose client protocol this
	// layer emulates. Clients gate features on it, so it is not the Jelee
	// build version.
	protocolVersion = "10.11.0"
	// encoderLocation is the upstream enum value for "no encoder": this
	// server never transcodes (G10.1).
	encoderLocation = "NotFound"
)

// publicSystemInfo mirrors the upstream public system information DTO.
// LocalAddress is deliberately absent: upstream fills it with an internal
// URL, and G11.6 forbids publishing internal addresses. Absent and null are
// the same on the wire because upstream omits null members.
type publicSystemInfo struct {
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// systemInfo mirrors the upstream system information DTO. Path members
// (program data, web, log, cache, metadata, transcoding temp), PackageName
// and SystemArchitecture are omitted: they are null-able upstream and would
// publish host details (G11.6). Capabilities is a Jelee extension member;
// unknown members are ignored by clients.
type systemInfo struct {
	publicSystemInfo
	OperatingSystemDisplayName string         `json:"OperatingSystemDisplayName"`
	HasPendingRestart          bool           `json:"HasPendingRestart"`
	IsShuttingDown             bool           `json:"IsShuttingDown"`
	SupportsLibraryMonitor     bool           `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber        int            `json:"WebSocketPortNumber"`
	CompletedInstallations     []struct{}     `json:"CompletedInstallations"`
	CanSelfRestart             bool           `json:"CanSelfRestart"`
	CanLaunchWebBrowser        bool           `json:"CanLaunchWebBrowser"`
	CastReceiverApplications   []struct{}     `json:"CastReceiverApplications"`
	HasUpdateAvailable         bool           `json:"HasUpdateAvailable"`
	EncoderLocation            string         `json:"EncoderLocation"`
	Capabilities               capabilityInfo `json:"JeleeCapabilities"`
}

// capabilityInfo declares the G10.4 / G24.4 capability set. Every member is
// false: transformation and adaptive streaming are never offered, and removed
// feature families stay hidden. It is a fixed value, not configuration.
type capabilityInfo struct {
	Transcoding bool `json:"Transcoding"`
	Remux       bool `json:"Remux"`
	Hls         bool `json:"Hls"`
	Dash        bool `json:"Dash"`
	LiveTv      bool `json:"LiveTv"`
	Channels    bool `json:"Channels"`
	Dlna        bool `json:"Dlna"`
	Downloads   bool `json:"Downloads"`
}

func (rt *router) systemRoutes() {
	rt.handle(http.MethodGet, "/System/Info/Public", false, rt.publicInfo)
	rt.handle(http.MethodGet, "/System/Info", true, rt.info)
	rt.handle(http.MethodGet, "/System/Ping", false, ping)
	rt.handle(http.MethodPost, "/System/Ping", false, ping)
}

// public is served without authentication. The HTTP server has no setup
// gate, so a server reachable here never asks clients to run a wizard.
func (rt *router) public() publicSystemInfo {
	return publicSystemInfo{
		ServerName:             serverName,
		Version:                protocolVersion,
		ProductName:            productName,
		ID:                     rt.opts.ServerID,
		StartupWizardCompleted: true,
	}
}

func (rt *router) publicInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, rt.public())
}

// info requires a native session. It reports that the server cannot restart
// itself (there is no restart route) and has no live library monitor or
// web socket, rather than the upstream constants.
func (rt *router) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, systemInfo{
		publicSystemInfo:         rt.public(),
		CompletedInstallations:   []struct{}{},
		CastReceiverApplications: []struct{}{},
		EncoderLocation:          encoderLocation,
	})
}

// ping answers both methods with the product name as a JSON string.
func ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, productName)
}
