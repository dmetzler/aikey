package webui

// page is the settings UI. It is a single self-contained document: no CDN, no
// build step, nothing fetched from the network.
const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>aikey settings</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{color-scheme:light dark}
body{font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
     max-width:640px;margin:2rem auto;padding:0 1.2rem}
h1{font-size:1.35rem;margin:0 0 .2rem}
.sub{color:#888;margin:0 0 1.5rem;font-size:.9rem}
fieldset{border:1px solid #8884;border-radius:8px;margin:0 0 1.2rem;padding:1rem}
legend{padding:0 .4rem;font-weight:600;font-size:.9rem}
label{display:block;margin:.7rem 0 .2rem;font-size:.85rem;color:#888}
input[type=text]{width:100%;padding:.5rem;border:1px solid #8886;border-radius:6px;
     background:transparent;color:inherit;font:inherit;box-sizing:border-box}
button{padding:.5rem 1rem;border:1px solid #8886;border-radius:6px;background:transparent;
     color:inherit;font:inherit;cursor:pointer;margin-right:.5rem}
button.primary{background:#2563eb;border-color:#2563eb;color:#fff}
button:disabled{opacity:.5;cursor:default}
.row{display:flex;align-items:center;justify-content:space-between;gap:1rem}
.pill{font-size:.8rem;padding:.15rem .6rem;border-radius:999px;border:1px solid currentColor}
.ok{color:#16a34a}.warn{color:#ca8a04}.err{color:#dc2626}
.muted{color:#888;font-size:.8rem;margin-top:.4rem;word-break:break-all}
#msg{margin:1rem 0;padding:.6rem .8rem;border-radius:6px;display:none}
#msg.show{display:block}
</style></head><body>
<h1>aikey</h1>
<p class="sub" id="profile-line">loading…</p>

<fieldset><legend>Session</legend>
  <div class="row">
    <span id="session-state">…</span>
    <span id="session-pill" class="pill">…</span>
  </div>
  <p class="muted" id="backend-line"></p>
  <div style="margin-top:1rem">
    <button class="primary" id="btn-login">Log in</button>
    <button id="btn-logout">Log out</button>
  </div>
</fieldset>

<fieldset><legend>Endpoints</legend>
  <label for="issuer">Keycloak issuer</label>
  <input type="text" id="issuer" placeholder="https://.../realms/your-realm" spellcheck="false">
  <label for="client_id">Client ID</label>
  <input type="text" id="client_id" spellcheck="false">
  <label for="upstream">Upstream (LiteLLM)</label>
  <input type="text" id="upstream" placeholder="https://.../ai" spellcheck="false">
  <label for="listen">Listen address</label>
  <input type="text" id="listen" spellcheck="false">
  <p class="muted">Loopback addresses only. Anyone who can reach this port spends your tokens.</p>
  <div style="margin-top:1rem"><button class="primary" id="btn-save">Save</button></div>
</fieldset>

<fieldset><legend>Start at login</legend>
  <div class="row">
    <span>Launch aikey automatically</span>
    <button id="btn-auto">…</button>
  </div>
  <p class="muted" id="auto-line"></p>
</fieldset>

<div id="msg"></div>

<script>
let csrf = "";
const $ = id => document.getElementById(id);

function flash(text, kind) {
  const m = $("msg");
  m.textContent = text;
  m.className = "show " + (kind || "");
  m.style.background = kind === "err" ? "#dc262622" : "#16a34a22";
  if (kind !== "err") setTimeout(() => { m.className = ""; }, 4000);
}

async function api(path, method, body) {
  const r = await fetch(path, {
    method: method || "GET",
    headers: { "X-Aikey-CSRF": csrf, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || ("HTTP " + r.status));
  return j;
}

let autoOn = false;

async function refresh() {
  const s = await (await fetch("/_aikey/api/state")).json();
  csrf = s.csrf;
  $("profile-line").textContent = "profile: " + s.profile;
  $("issuer").value = s.issuer || "";
  $("client_id").value = s.client_id || "";
  $("upstream").value = s.upstream || "";
  $("listen").value = s.listen || "";
  $("backend-line").textContent = "token stored in: " + s.token_backend;

  const pill = $("session-pill");
  if (s.logged_in) {
    const mins = Math.floor(s.expires_in_seconds / 60);
    $("session-state").textContent = "Signed in";
    pill.textContent = mins > 0 ? ("expires in " + mins + " min") : "expired";
    pill.className = "pill " + (mins > 2 ? "ok" : "warn");
  } else {
    $("session-state").textContent = "Not signed in";
    pill.textContent = "no token";
    pill.className = "pill err";
  }

  autoOn = s.autostart;
  $("btn-auto").textContent = autoOn ? "Disable" : "Enable";
  $("auto-line").textContent = (autoOn ? "Registered at: " : "Would be written to: ")
    + s.autostart_location;
}

$("btn-login").onclick = async () => {
  try { flash("Opening your browser…"); await api("/_aikey/api/login", "POST");
        flash("Signed in.", "ok"); refresh(); }
  catch (e) { flash(e.message, "err"); }
};
$("btn-logout").onclick = async () => {
  try { await api("/_aikey/api/logout", "POST"); flash("Signed out.", "ok"); refresh(); }
  catch (e) { flash(e.message, "err"); }
};
$("btn-save").onclick = async () => {
  try {
    const r = await api("/_aikey/api/settings", "POST", {
      issuer: $("issuer").value.trim(),
      client_id: $("client_id").value.trim(),
      upstream: $("upstream").value.trim(),
      listen: $("listen").value.trim(),
    });
    flash(r.note || "Saved.", "ok"); refresh();
  } catch (e) { flash(e.message, "err"); }
};
$("btn-auto").onclick = async () => {
  try {
    await api("/_aikey/api/autostart", "POST", { enabled: !autoOn });
    flash(autoOn ? "Autostart disabled." : "Autostart enabled.", "ok"); refresh();
  } catch (e) { flash(e.message, "err"); }
};

refresh().catch(e => flash(e.message, "err"));
setInterval(() => refresh().catch(() => {}), 15000);
</script></body></html>
`
