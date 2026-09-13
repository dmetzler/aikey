// Package tray provides the optional system tray icon.
//
// Single binary, two build modes: the default build includes the tray, and
// `-tags headless` compiles a stub. That matters because the tray needs cgo and
// a desktop session; a server or container has neither, and `go build` must
// still work there.
package tray

import "time"

// State is the snapshot the tray renders.
type State struct {
	Profile  string
	LoggedIn bool
	Expiry   time.Time
	Listen   string
	Backend  string
}

// Callbacks wires the tray to the running agent.
type Callbacks struct {
	State           func() State
	Login           func() error
	Logout          func() error
	OpenSettings    func()
	AutostartStatus func() bool
	SetAutostart    func(bool) error
	Quit            func()
}
