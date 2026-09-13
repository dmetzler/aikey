// Command aikey is a loopback token agent: it holds a Keycloak OIDC session and
// injects a fresh bearer token into requests aimed at a LiteLLM proxy.
//
// No endpoint is baked in. Run `aikey init` to record yours.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmetzler/aikey/internal/config"
	"github.com/dmetzler/aikey/internal/oidc"
	"github.com/dmetzler/aikey/internal/proxy"
)

const usage = `aikey - keep a short-lived Keycloak token in front of your LLM proxy

Usage:
  aikey init [--profile NAME]   Ask for the endpoints and write the config file
  aikey login [--profile NAME]  Sign in through the browser (--device for headless)
  aikey serve [--profile NAME]  Run the loopback proxy
  aikey status [--profile NAME] Show the current session
  aikey logout [--profile NAME] Forget the stored token
  aikey profiles                List configured profiles

Configuration lives in the file printed by 'aikey status'. Environment
overrides: AIKEY_ISSUER, AIKEY_CLIENT_ID, AIKEY_UPSTREAM, AIKEY_LISTEN,
AIKEY_PROFILE, AIKEY_CONFIG.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	profile := fs.String("profile", "", "profile name")
	device := fs.Bool("device", false, "use the device flow (no local browser)")
	_ = fs.Parse(os.Args[2:])

	var err error
	switch cmd {
	case "init":
		err = cmdInit(*profile)
	case "login":
		err = cmdLogin(*profile, *device)
	case "serve":
		err = cmdServe(*profile)
	case "status":
		err = cmdStatus(*profile)
	case "logout":
		err = cmdLogout(*profile)
	case "profiles":
		err = cmdProfiles()
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func ask(r *bufio.Reader, prompt, current string) string {
	if current != "" {
		fmt.Printf("%s [%s]: ", prompt, current)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return current
	}
	return line
}

func cmdInit(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if name == "" {
		name = config.DefaultProfile
	}
	p := cfg.Profiles[name]
	if p == nil {
		p = &config.Profile{}
	}

	fmt.Printf("Configuring profile %q.\n", name)
	fmt.Println("aikey ships no default endpoints, so both URLs are required.")
	fmt.Println()

	r := bufio.NewReader(os.Stdin)
	p.Issuer = ask(r, "Keycloak realm URL (e.g. https://HOST/realms/REALM)", p.Issuer)
	p.ClientID = ask(r, "Keycloak public client ID", p.ClientID)
	p.Upstream = ask(r, "LiteLLM base URL (e.g. https://HOST/ai)", p.Upstream)
	p.Listen = ask(r, "Local listen address", firstNonEmpty(p.Listen, config.DefaultListen))

	if err := p.Validate(); err != nil {
		return err
	}

	cfg.Profiles[name] = p
	if cfg.Current == "" {
		cfg.Current = name
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	path, _ := config.Path()
	fmt.Printf("\nWrote %s\n", path)
	fmt.Println("Next: aikey login")
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolve(name string) (*config.Profile, string, *oidc.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", nil, err
	}
	p, resolved, err := cfg.Resolve(name)
	if err != nil {
		return nil, "", nil, err
	}
	path, err := config.Path()
	if err != nil {
		return nil, "", nil, err
	}
	return p, resolved, oidc.NewStore(path, resolved), nil
}

func cmdLogin(name string, device bool) error {
	p, resolved, store, err := resolve(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ep, err := oidc.Discover(ctx, p.Issuer)
	if err != nil {
		return err
	}

	var tok *oidc.Token
	if device {
		dc, err := oidc.StartDevice(ctx, ep, p.ClientID)
		if err != nil {
			return err
		}
		fmt.Printf("Open %s and enter code: %s\n", dc.VerificationURI, dc.UserCode)
		if dc.Complete != "" {
			fmt.Printf("Or go straight to: %s\n", dc.Complete)
		}
		tok, err = oidc.PollDevice(ctx, ep, p.ClientID, dc)
		if err != nil {
			return err
		}
	} else {
		tok, err = oidc.Login(ctx, ep, p.ClientID)
		if err != nil {
			return err
		}
	}

	if err := store.Save(tok); err != nil {
		return err
	}
	fmt.Printf("Logged in on profile %q. Access token valid until %s.\n",
		resolved, tok.ExpiresAt.Format(time.Kitchen))
	fmt.Println("Start the proxy with: aikey serve")
	return nil
}

func cmdServe(name string) error {
	p, _, store, err := resolve(name)
	if err != nil {
		return err
	}
	ep, err := oidc.Discover(context.Background(), p.Issuer)
	if err != nil {
		return err
	}
	ts, err := proxy.NewTokenSource(ep, p.ClientID, store)
	if err != nil {
		return err
	}
	srv, err := proxy.New(p.Listen, p.Upstream, ts)
	if err != nil {
		return err
	}

	fmt.Printf("Point your tools at it:\n")
	fmt.Printf("  export OPENAI_BASE_URL=http://%s\n", p.Listen)
	fmt.Printf("  export OPENAI_API_KEY=aikey\n\n")
	return srv.ListenAndServe()
}

func cmdStatus(name string) error {
	path, _ := config.Path()
	fmt.Printf("config: %s\n", path)

	p, resolved, store, err := resolve(name)
	if err != nil {
		fmt.Printf("profile: %s\n", resolved)
		return err
	}
	fmt.Printf("profile: %s\n", resolved)
	fmt.Printf("issuer:   %s\n", p.Issuer)
	fmt.Printf("client:   %s\n", p.ClientID)
	fmt.Printf("upstream: %s\n", p.Upstream)
	fmt.Printf("listen:   %s\n", p.Listen)

	tok, err := store.Load()
	if err != nil {
		return err
	}
	if tok == nil {
		fmt.Println("session:  logged out (run `aikey login`)")
		return nil
	}
	if tok.Expired(0) {
		fmt.Printf("session:  access token expired %s ago (will refresh on next request)\n",
			time.Since(tok.ExpiresAt).Truncate(time.Second))
		return nil
	}
	fmt.Printf("session:  valid for %s\n", time.Until(tok.ExpiresAt).Truncate(time.Second))
	return nil
}

func cmdLogout(name string) error {
	_, resolved, store, err := resolve(name)
	if err != nil {
		return err
	}
	if err := store.Delete(); err != nil {
		return err
	}
	fmt.Printf("Cleared the stored token for profile %q.\n", resolved)
	return nil
}

func cmdProfiles() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Profiles) == 0 {
		fmt.Println("No profiles yet. Run `aikey init`.")
		return nil
	}
	for name, p := range cfg.Profiles {
		marker := "  "
		if name == cfg.Current {
			marker = "* "
		}
		fmt.Printf("%s%-12s %s -> %s\n", marker, name, p.ClientID, p.Upstream)
	}
	return nil
}
