# aikey

Keep a short-lived Keycloak token in front of your LLM proxy.

`aikey` runs a small loopback HTTP proxy on your machine. Point any OpenAI-compatible
tool at it, and it injects a freshly refreshed OIDC access token into every request.
Your tools never see the real token, and you never paste one into a config file again.

The point is **real revocation**: disable the user in Keycloak and access stops at the
next refresh. A static API key would keep working forever.

## Status

Spike, and the spike succeeded: validated end to end against a live LiteLLM +
Keycloak deployment (plain completion, genuinely incremental streaming, and a
Strands agent driving it unmodified). Still not a finished product — see
[Limitations](#limitations).

## No default endpoints, on purpose

This repository is public and ships **no** Keycloak or LiteLLM URL. Both are required
and must come from your own config. `aikey` refuses to start without them.

## Install

```sh
go build -o aikey ./cmd/aikey
```

## Use

```sh
aikey init      # asks for your endpoints, writes ~/.config/aikey/config.json (0600)
aikey login     # browser sign-in (PKCE); use --device on a headless box
aikey serve     # starts the loopback proxy
```

Then point your tools at it:

```sh
export OPENAI_BASE_URL=http://127.0.0.1:4001
export OPENAI_API_KEY=aikey     # any value; it is stripped and replaced
```

Check the session at any time:

```sh
aikey status
curl -s http://127.0.0.1:4001/_aikey/health
```

## Validate it works

```sh
pip install 'strands-agents[openai]'        # optional; the script skips it if absent
python3 examples/check_aikey.py --model gpt-4o-mini
```

It checks the session, a plain completion, a **streaming** completion (timing the
chunks to tell real streaming from buffering), and a Strands agent.

## Profiles

Several deployments from one install:

```sh
aikey init --profile work
aikey login --profile work
aikey serve --profile work
aikey profiles
```

`AIKEY_PROFILE` selects one for a single command.

## Configuration

`aikey init` writes the file for you. Environment variables override it, which is
handy for CI and one-off runs:

| Variable | Meaning |
| --- | --- |
| `AIKEY_ISSUER` | Keycloak realm URL, e.g. `https://HOST/realms/REALM` |
| `AIKEY_CLIENT_ID` | public OIDC client ID |
| `AIKEY_UPSTREAM` | LLM proxy base URL |
| `AIKEY_LISTEN` | listen address (default `127.0.0.1:4001`) |
| `AIKEY_PROFILE` | profile to use |
| `AIKEY_CONFIG` | config file path |

## Keycloak client

Create a **public** client with PKCE (`S256`) and standard flow enabled.

Redirect URIs must use a **trailing** wildcard, because the loopback port is
ephemeral (RFC 8252):

```
http://127.0.0.1:*
http://localhost:*
```

A wildcard in the middle (`http://127.0.0.1:*/callback`) matches nothing in Keycloak
and yields `Invalid parameter: redirect_uri`.

## Security

- Binds loopback only, and **refuses** to start on any other address: anything that can
  reach the port can spend your tokens.
- Any client-supplied `Authorization` / `X-Api-Key` header is stripped before proxying.
- Config and token files are written `0600`.
- Tokens are refreshed with a 60s margin so a long stream never starts on a dying token.

> Any local process can call the proxy. This is the same trust model as `ssh-agent`:
> fine on a personal machine, not on a shared host.

## Limitations

Known gaps, honestly:

- **Tokens are stored in a `0600` file, not the OS keyring.** Keyring support needs cgo
  and platform libraries; that was out of scope for a spike.
- **Streaming works but is not guarded by the test suite.** Verified by hand against a
  live upstream (46 chunks spread over 0.18s). The Go streaming tests still pass when
  flushing is deliberately broken, so they document intent rather than protect it.
- **A refresh has never been exercised mid-stream.** Long generations that outlive the
  access token are the one path still untested.
- No tray icon, no web UI, no spend display.
- Refresh is lazy (on request), so the first call after a long idle pays the latency.
- Spend must be read from the proxy, never recomputed locally.

## License

MIT
