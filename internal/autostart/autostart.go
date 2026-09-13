// Package autostart registers aikey to start at login.
//
// This writes into the OS's startup mechanism, so it is never enabled
// implicitly: the user asks for it explicitly, from the tray menu or the CLI.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Label / identifiers used across platforms.
const (
	label   = "io.github.dmetzler.aikey"
	winName = "aikey"
)

// Status reports whether autostart is currently registered.
func Status() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		p, err := plistPath()
		if err != nil {
			return false, err
		}
		return exists(p), nil
	case "linux":
		p, err := desktopPath()
		if err != nil {
			return false, err
		}
		return exists(p), nil
	case "windows":
		out, err := exec.Command("reg", "query",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", winName).CombinedOutput()
		if err != nil {
			// A missing value exits non-zero; that is "not enabled", not a failure.
			return false, nil
		}
		return strings.Contains(string(out), winName), nil
	default:
		return false, unsupported()
	}
}

// Enable registers aikey to run at login, serving the given profile.
func Enable(profile string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		return enableDarwin(exe, profile)
	case "linux":
		return enableLinux(exe, profile)
	case "windows":
		return enableWindows(exe, profile)
	default:
		return unsupported()
	}
}

// Disable removes the autostart registration.
func Disable() error {
	switch runtime.GOOS {
	case "darwin":
		p, err := plistPath()
		if err != nil {
			return err
		}
		// Best-effort unload; the file removal is what actually matters.
		_ = exec.Command("launchctl", "unload", p).Run()
		return removeIfExists(p)
	case "linux":
		p, err := desktopPath()
		if err != nil {
			return err
		}
		return removeIfExists(p)
	case "windows":
		return exec.Command("reg", "delete",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", winName, "/f").Run()
	default:
		return unsupported()
	}
}

// Location returns the file or registry key that Enable would write, so the UI
// can tell the user exactly what is being touched.
func Location() string {
	switch runtime.GOOS {
	case "darwin":
		p, err := plistPath()
		if err != nil {
			return "(unknown)"
		}
		return p
	case "linux":
		p, err := desktopPath()
		if err != nil {
			return "(unknown)"
		}
		return p
	case "windows":
		return `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\` + winName
	default:
		return "(unsupported)"
	}
}

func unsupported() error {
	return fmt.Errorf("autostart is not supported on %s", runtime.GOOS)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func removeIfExists(p string) error {
	err := os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// bundlePath returns the enclosing .app when exe lives inside one.
//
// A LaunchAgent pointing at Contents/MacOS/aikey starts the binary outside its
// bundle: macOS then treats it as an anonymous process, and the menu bar icon
// loses the app identity (and its icon). Registering the bundle keeps it whole.
func bundlePath(exe string) (string, bool) {
	dir := filepath.Dir(exe) // .../Foo.app/Contents/MacOS
	if filepath.Base(dir) != "MacOS" {
		return "", false
	}
	contents := filepath.Dir(dir)
	if filepath.Base(contents) != "Contents" {
		return "", false
	}
	app := filepath.Dir(contents)
	if filepath.Ext(app) != ".app" {
		return "", false
	}
	return app, true
}

func enableDarwin(exe, profile string) error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}

	// Prefer `open -a Bundle.app` so the agent keeps its bundle identity.
	args := []string{exe, "serve"}
	if app, ok := bundlePath(exe); ok {
		args = []string{"/usr/bin/open", "-a", app, "--args", "serve"}
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	sb.WriteString(`<plist version="1.0"><dict>` + "\n")
	sb.WriteString("  <key>Label</key><string>" + label + "</string>\n")
	sb.WriteString("  <key>ProgramArguments</key><array>\n")
	for _, a := range args {
		sb.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	sb.WriteString("  </array>\n")
	sb.WriteString("  <key>RunAtLoad</key><true/>\n")
	sb.WriteString("  <key>KeepAlive</key><true/>\n")
	sb.WriteString("</dict></plist>\n")

	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	// Load now so the user does not have to log out to see it work.
	_ = exec.Command("launchctl", "load", p).Run()
	return nil
}

func desktopPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "aikey.desktop"), nil
}

func enableLinux(exe, profile string) error {
	p, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	cmd := exe + " serve"
	if profile != "" {
		cmd += " --profile " + profile
	}
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=aikey\n" +
		"Comment=Keycloak token agent for LLM proxies\n" +
		"Exec=" + cmd + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(p, []byte(content), 0o644)
}

func enableWindows(exe, profile string) error {
	cmd := `"` + exe + `" serve`
	if profile != "" {
		cmd += " --profile " + profile
	}
	return exec.Command("reg", "add",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", winName, "/t", "REG_SZ", "/d", cmd, "/f").Run()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
