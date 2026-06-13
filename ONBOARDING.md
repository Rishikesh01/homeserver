# Onboarding a person / device

A short, repeatable checklist for adding a family member (and each of their
devices) to the homeserver. Do the **one-time cert step** on every device, then
the per-app steps. Hand someone this page and they can self-serve most of it.

Below, replace **`SERVER_IP`** with your server's LAN address and use the host ports
your setup chose — both are printed in **`WELCOME.txt`** (created by `bootstrap.sh`), or
ask your admin. Friendly names (need the router to hand out Pi-hole as DNS — your admin sets that up):
`vault.lan` (passwords), `cloud.lan` (files), `pihole.lan` (ad-block admin).

---

## 1. Install the certificate (once per device) — do this first

Because the names end in `.lan` (not a public domain), the server runs its own
certificate authority. Each device must trust it once, or browsers warn and the
**Bitwarden / Nextcloud apps will refuse to connect**.

**Get the cert:** on the home WiFi, open **http://SERVER_IP/** in a browser
(or **http://ca.lan/** if the device already uses Pi-hole DNS) and tap **Download
root.crt**. That's the whole transfer step — no AirDrop/USB needed. Then install it:

- **Android:** Settings → Security → *Encryption & credentials* → *Install a
  certificate* → **CA certificate** → pick the file → accept the warning.
- **iPhone/iPad:** open the file → *Settings → Profile Downloaded → Install*. Then
  **Settings → General → About → Certificate Trust Settings** → turn it **on**.
- **Windows:** double-click → *Install Certificate* → **Local Machine** → *Place in*
  → **Trusted Root Certification Authorities**.
- **macOS:** open in Keychain Access → **System** → double-click the cert → *Trust*
  → **Always Trust**.

> If you skip this, everything still works **by IP** (e.g. `http://SERVER_IP:8080`)
> — you just get cert warnings on the `vault.lan` style names.

---

## 2. Passwords — Vaultwarden (Bitwarden app)

1. On the home WiFi, open **https://vault.lan** — or **http://SERVER_IP:8080** if you
   haven't installed the cert yet.
2. **Create account** → email + a strong master password. **The master password is
   the one thing nobody can reset for you — write it down somewhere safe.**
3. Phone: install **Bitwarden** (App Store / Play Store). On the login screen tap
   the gear/settings → **Self-hosted** → Server URL `https://vault.lan` → log in.
   (The cert from step 1 must be installed for the app to connect.)

---

## 3. Files — Nextcloud

1. Open **https://cloud.lan** (or `http://SERVER_IP:8081`). Ask the admin to
   create your user, or log in with the one you were given.
2. Phone: install **Nextcloud** → server `https://cloud.lan` → log in. Enable
   *Auto upload* for photos if you want phone backups.

> These work on the **home network** only (there's no VPN). For `*.lan` to resolve on
> every device, the router hands out the server as DNS — your admin sets that up once.

---

## Admin notes (server owner)

- Cert file comes from `hsctl get-ca` (writes `caddy-root-ca.crt`); the server also
  serves it at `http://SERVER_IP/root.crt`.
- `*.lan` resolves LAN-wide once the router's DHCP DNS points at the server (see the
  DNS strategy in README.md).
- Nextcloud users: create them in *Admin → Users*. Vaultwarden currently allows
  self-signup (open registration) — switch to invitation-only later by setting
  `VW_SIGNUPS_ALLOWED=false` in `vaultwarden/.env` and re-running `hsctl up`, then
  invite people from the org settings.
