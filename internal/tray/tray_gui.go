//go:build !headless

package tray

import (
	"fmt"
	"time"

	"fyne.io/systray"
)

// Supported reports whether this build can show a tray icon.
func Supported() bool { return true }

// Run displays the tray icon and blocks until Quit. It must run on the main
// goroutine: macOS requires UI work on the first thread.
func Run(cb Callbacks) {
	systray.Run(func() { onReady(cb) }, func() {})
}

func onReady(cb Callbacks) {
	systray.SetTemplateIcon(iconData, iconData)
	systray.SetTitle("")
	systray.SetTooltip("aikey")

	mStatus := systray.AddMenuItem("…", "Current session")
	mStatus.Disable()
	mProfile := systray.AddMenuItem("…", "Active profile")
	mProfile.Disable()
	systray.AddSeparator()

	mSettings := systray.AddMenuItem("Settings…", "Open the settings page")
	mLogin := systray.AddMenuItem("Log in", "Sign in to Keycloak")
	mLogout := systray.AddMenuItem("Log out", "Forget the stored token")
	systray.AddSeparator()

	// Deliberately off by default: writing to the OS startup mechanism is the
	// user's call, never a side effect of launching the app.
	mAuto := systray.AddMenuItemCheckbox("Start at login", "Register aikey to run at login", false)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit aikey", "Stop the agent")

	refresh := func() {
		s := cb.State()
		if s.LoggedIn {
			d := time.Until(s.Expiry)
			if d > 0 {
				mStatus.SetTitle(fmt.Sprintf("● Signed in — %dm left", int(d.Minutes())))
			} else {
				mStatus.SetTitle("● Token expired — refreshing")
			}
		} else {
			mStatus.SetTitle("○ Not signed in")
		}
		mProfile.SetTitle(fmt.Sprintf("%s — %s", s.Profile, s.Listen))
		if cb.AutostartStatus() {
			mAuto.Check()
		} else {
			mAuto.Uncheck()
		}
	}
	refresh()

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-mSettings.ClickedCh:
				cb.OpenSettings()
			case <-mLogin.ClickedCh:
				go func() { _ = cb.Login(); refresh() }()
			case <-mLogout.ClickedCh:
				go func() { _ = cb.Logout(); refresh() }()
			case <-mAuto.ClickedCh:
				go func() {
					_ = cb.SetAutostart(!cb.AutostartStatus())
					refresh()
				}()
			case <-mQuit.ClickedCh:
				cb.Quit()
				systray.Quit()
				return
			}
		}
	}()
}
