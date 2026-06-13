# Onboarding a person / device

A short, repeatable checklist for adding a family member (and each of their
devices) to the homeserver. Do the **one-time cert step** on every device, then
the per-app steps. Hand someone this page and they can self-serve most of it.

Server LAN IP: **192.168.0.150**. Friendly names (need Pi-hole DNS or the VPN):
`vault.lan` (passwords), `cloud.lan` (files), `pihole.lan` (ad-block admin).

---

## 1. Install the certificate (once per device) — do this first

Because the names end in `.lan` (not a public domain), the server runs its own
certificate authority. Each device must trust it once, or browsers warn and the
**Bitwarden / Nextcloud apps will refuse to connect**.

**Get the cert:** on the home WiFi, open **http://192.168.0.150/** in a browser
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

> If you skip this, everything still works **by IP** (e.g. `http://192.168.0.150:8082`)
> — you just get cert warnings on the `vault.lan` style names.

---

## 2. Passwords — Vaultwarden (Bitwarden app)

1. On the home WiFi (or with the VPN on), open **https://vault.lan** — or
   **http://192.168.0.150:8082** if you haven't installed the cert yet.
2. **Create account** → email + a strong master password. **The master password is
   the one thing nobody can reset for you — write it down somewhere safe.**
3. Phone: install **Bitwarden** (App Store / Play Store). On the login screen tap
   the gear/settings → **Self-hosted** → Server URL `https://vault.lan` → log in.
   (The cert from step 1 must be installed for the app to connect.)

---

## 3. Files — Nextcloud

1. Open **https://cloud.lan** (or `http://192.168.0.150:8081`). Ask the admin to
   create your user, or log in with the one you were given.
2. Phone: install **Nextcloud** → server `https://cloud.lan` → log in. Enable
   *Auto upload* for photos if you want phone backups.

---

## 4. Remote access (away from home) — WireGuard VPN

Only needed to reach the services when you're **not** on the home WiFi. While the
tunnel is on, the `.lan` names resolve and ad-blocking works; everything else uses
your normal connection (split tunnel), so your internet keeps working even if the
server is down.

1. Install the **WireGuard** app (App Store / Play Store, or wireguard.com for a
   laptop).
2. The admin opens the wg-easy panel (`http://192.168.0.150:51821`), clicks **New
   Client**, names it after your device, and shows you the **QR code**.
3. In the WireGuard app: **＋ → Scan from QR code** → scan it → toggle the tunnel on.
4. Test: load **https://vault.lan**. Turn the tunnel off when you don't need it.

> Do **not** turn on any VPN "kill switch" / "block connections without VPN" — that's
> the one setting that would break your internet if the server is offline.

---

## Admin notes (server owner)

- Cert file comes from `./get-ca.sh` (writes `caddy-root-ca.crt`).
- New VPN client = wg-easy panel → New Client → share the QR. One per device.
- Nextcloud users: create them in *Admin → Users*. Vaultwarden currently allows
  self-signup (open registration) — switch to invitation-only later by setting
  `VW_SIGNUPS_ALLOWED=false` in `vaultwarden/.env` and re-running `./up.sh`, then
  invite people from the org settings.
