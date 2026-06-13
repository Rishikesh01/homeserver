# Onboarding a person / device

A short checklist for adding someone (and each of their devices) to the homeserver. Do
the **one-time certificate step** on every device, then the per-app steps. Hand someone
this page and they can self-serve most of it.

Everything is reached over HTTPS at the **server's IP + a port** (no names). Replace
**`SERVER_IP`** below with the server's LAN address (ask your admin; it's also on the
dashboard). The **dashboard** at `https://SERVER_IP` links to every app.

| | URL |
|--|--|
| 🏠 Dashboard (start here) | `https://SERVER_IP` |
| 🔑 Passwords (Vaultwarden) | `https://SERVER_IP:8443` |
| ☁️ Files (Nextcloud) | `https://SERVER_IP:8444` |
| 🛡️ Pi-hole | `https://SERVER_IP:8445/admin` |

---

## 1. Install the certificate (once per device) — do this first

The server runs its own certificate authority (there's no public domain). Each device
must trust it once, or browsers warn and the **Bitwarden / Nextcloud apps refuse to
connect**.

**Get the cert:** on the home WiFi, open **http://SERVER_IP/** in a browser and tap
**Download root.crt**. Then install it:

- **Android:** Settings → Security → *Encryption & credentials* → *Install a
  certificate* → **CA certificate** → pick the file → accept the warning.
- **iPhone/iPad:** open the file → *Settings → Profile Downloaded → Install*. Then
  **Settings → General → About → Certificate Trust Settings** → turn it **on**.
- **Windows:** double-click → *Install Certificate* → **Local Machine** → *Place in*
  → **Trusted Root Certification Authorities**.
- **macOS:** open in Keychain Access → **System** → double-click the cert → *Trust*
  → **Always Trust**.

---

## 2. Passwords — Vaultwarden (Bitwarden app)

1. Open **https://SERVER_IP:8443**.
2. **Create account** → email + a strong master password. **The master password is the
   one thing nobody can reset for you — write it down somewhere safe.**
3. Phone: install **Bitwarden** (App Store / Play Store). On the login screen tap the
   gear/settings → **Self-hosted** → Server URL `https://SERVER_IP:8443` → log in.
   (The cert from step 1 must be installed for the app to connect.)

---

## 3. Files — Nextcloud

1. Open **https://SERVER_IP:8444**. Ask the admin to create your user, or log in with the
   one you were given.
2. Phone: install **Nextcloud** → server `https://SERVER_IP:8444` → log in. Enable
   *Auto upload* for photos if you want phone backups.

> These work on the **home network** only (there's no VPN).

---

## Admin notes (server owner)

- Cert: `hsctl get-ca` writes `caddy-root-ca.crt`; the server also serves it at
  `http://SERVER_IP/root.crt`.
- Pi-hole is just a network ad-blocker now — point the router's DHCP DNS at the server to
  ad-block every device (see README.md → "Pi-hole ad-blocking").
- Add an app to the dashboard: add a service folder + a Caddy block (new HTTPS port) + an
  entry in `services.json`. The dashboard updates on next load.
- Nextcloud users: create them in *Admin → Users*. Vaultwarden allows self-signup; switch
  to invitation-only later via `VW_SIGNUPS_ALLOWED=false` + `hsctl up`.
