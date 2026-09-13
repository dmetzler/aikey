#!/usr/bin/env python3
"""Validate an aikey loopback proxy.

Usage:
    python3 check_aikey.py [--base-url http://127.0.0.1:4001] [--model gpt-4o-mini]

Runs three checks:
  1. health + a non-streaming call   (does token injection work at all?)
  2. a STREAMING call, timing chunks (does the proxy stream, or buffer?)
  3. the same through Strands        (does a real SDK drive it?)

Check 2 is the one that matters: the Go test suite does NOT prove streaming
works, so this is where it gets settled.
"""
import argparse, json, sys, time, urllib.request, urllib.error

DUMMY_KEY = "aikey"  # stripped and replaced by the proxy


def post(base, path, payload, stream=False, timeout=120):
    req = urllib.request.Request(
        base.rstrip("/") + path,
        data=json.dumps(payload).encode(),
        headers={"Authorization": f"Bearer {DUMMY_KEY}",
                 "Content-Type": "application/json",
                 "Accept": "text/event-stream" if stream else "application/json"},
    )
    return urllib.request.urlopen(req, timeout=timeout)


def fail(msg, hint=""):
    print(f"  FAIL  {msg}")
    if hint:
        print(f"        -> {hint}")
    return False


def check_health(base):
    print("[1/3] health + non-streaming call")
    try:
        with urllib.request.urlopen(base.rstrip("/") + "/_aikey/health", timeout=10) as r:
            h = json.load(r)
    except Exception as e:
        return fail(f"cannot reach {base}: {e}", "is `aikey serve` running?")

    if h.get("status") == "logged_out":
        return fail("aikey is logged out", "run `aikey login`")
    print(f"  ok    session valid for {h.get('expires_in_seconds')}s")
    return True


def check_completion(base, model):
    try:
        with post(base, "/v1/chat/completions", {
            "model": model,
            "messages": [{"role": "user", "content": "Reply with exactly: OK"}],
            "max_tokens": 10,
        }) as r:
            body = json.load(r)
        msg = body["choices"][0]["message"]["content"].strip()
        print(f"  ok    model replied: {msg!r}")
        return True
    except urllib.error.HTTPError as e:
        detail = e.read().decode()[:300]
        if e.code == 401:
            return fail(f"401 from the proxy: {detail}",
                        "token rejected upstream, or aikey needs `aikey login`")
        return fail(f"HTTP {e.code}: {detail}")
    except Exception as e:
        return fail(str(e))


def check_streaming(base, model):
    print("[2/3] streaming (the spike's open question)")
    payload = {
        "model": model,
        "messages": [{"role": "user",
                      "content": "Count slowly from 1 to 20, one number per line."}],
        "stream": True,
        "max_tokens": 300,
    }
    t0 = time.monotonic()
    stamps, text = [], []
    try:
        resp = post(base, "/v1/chat/completions", payload, stream=True)
        for raw in resp:
            line = raw.decode("utf-8", "replace").strip()
            if not line.startswith("data:"):
                continue
            data = line[5:].strip()
            if data == "[DONE]":
                break
            try:
                delta = json.loads(data)["choices"][0].get("delta", {})
            except Exception:
                continue
            piece = delta.get("content")
            if piece:
                stamps.append(time.monotonic() - t0)
                text.append(piece)
    except urllib.error.HTTPError as e:
        return fail(f"HTTP {e.code}: {e.read().decode()[:300]}")
    except Exception as e:
        return fail(str(e))

    if len(stamps) < 2:
        return fail(f"only {len(stamps)} chunk(s) received",
                    "cannot tell streaming from buffering; try a longer prompt")

    first, last = stamps[0], stamps[-1]
    spread = last - first
    print(f"  ok    {len(stamps)} chunks | first at {first:.2f}s | last at {last:.2f}s")

    # If every chunk lands within a few ms of the last one, the proxy held the
    # whole response and flushed it at the end: that is buffering, not streaming.
    if spread < 0.05:
        return fail(f"all chunks arrived within {spread*1000:.0f}ms of each other",
                    "the response was BUFFERED, not streamed -- check FlushInterval")
    print(f"  ok    chunks spread over {spread:.2f}s -> genuinely streaming")
    return True


def check_strands(base, model):
    print("[3/3] Strands agent")
    try:
        from strands import Agent
        from strands.models.openai import OpenAIModel
    except ImportError:
        print("  skip  strands not installed (pip install 'strands-agents[openai]')")
        return None

    try:
        m = OpenAIModel(
            client_args={"api_key": DUMMY_KEY, "base_url": base.rstrip("/") + "/v1"},
            model_id=model,
            params={"max_tokens": 200, "temperature": 0.2},
        )
        agent = Agent(model=m)
        resp = agent("In one short sentence, what is a loopback interface?")
        print(f"  ok    agent replied: {str(resp).strip()[:120]}")
        return True
    except Exception as e:
        return fail(f"{type(e).__name__}: {e}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:4001")
    ap.add_argument("--model", default="gpt-4o-mini")
    a = ap.parse_args()

    print(f"aikey at {a.base_url}, model {a.model}\n")
    results = []
    if not check_health(a.base_url):
        sys.exit(1)
    results.append(check_completion(a.base_url, a.model))
    results.append(check_streaming(a.base_url, a.model))
    results.append(check_strands(a.base_url, a.model))

    print()
    hard = [r for r in results if r is not None]
    if all(hard):
        print("All checks passed. The loopback proxy holds up.")
        sys.exit(0)
    print("Some checks failed (see above).")
    sys.exit(1)


if __name__ == "__main__":
    main()
