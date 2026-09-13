//go:build headless

package tray

// Supported reports whether this build can show a tray icon.
func Supported() bool { return false }

// Run is a no-op in headless builds; `serve` runs without a tray.
func Run(cb Callbacks) {}
