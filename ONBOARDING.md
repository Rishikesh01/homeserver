# Getting set up on the home server

A short guide for adding a person (and each of their devices) to the home server. Do the
**certificate step** once on every device, then set up the apps you want.

The server's address is **SERVER_IP**. Everything is reached at that address plus a port, over
HTTPS — and only on the home network.

| | Open | What it's for |
|--|--|--|
| 🏠 Home page | [https://SERVER_IP](https://SERVER_IP) | links to everything below — start here |
| 🔑 Passwords | [https://SERVER_IP:8443](https://SERVER_IP:8443) | Vaultwarden — works with the Bitwarden apps |
| ☁️ Files | [https://SERVER_IP:8444](https://SERVER_IP:8444) | Nextcloud — files, photos, calendar |
| 📄 PDF tools | [https://SERVER_IP:8446](https://SERVER_IP:8446) | compress, merge, split, convert PDFs |
| 🧰 Utilities | [https://SERVER_IP:8447](https://SERVER_IP:8447) | converters, QR codes, hashes |
| 🖼️ Image tool | [https://SERVER_IP:8448](https://SERVER_IP:8448) | resize / compress / convert images, in your browser |
| 🛡️ Ad blocker | [https://SERVER_IP:8445/admin](https://SERVER_IP:8445/admin) | Pi-hole's admin page, if your server runs it |

---

## 1. Install the certificate (once per device) — do this first

The server runs its own certificate authority (there's no public domain). Each device must
trust it once, or browsers show warnings and the **Bitwarden and Nextcloud apps refuse to
connect**.

**Get it:** on the home Wi-Fi, open [http://SERVER_IP/](http://SERVER_IP/) (plain `http`) and tap
**Download the certificate** — the file is `homeserver-ca.crt`. Then install it:

- **Android:** Settings → Security → *Encryption & credentials* → *Install a certificate* →
  **CA certificate** → pick `homeserver-ca.crt` → accept the warning.
- **iPhone/iPad:** open the downloaded file → *Settings → Profile Downloaded → Install*. Then
  **Settings → General → About → Certificate Trust Settings** → turn it **on**.
- **Windows:** double-click `homeserver-ca.crt` → *Install Certificate* → **Local Machine** →
  *Place in* → **Trusted Root Certification Authorities**.
- **macOS:** open it in Keychain Access → **System** → double-click the certificate → *Trust* →
  **Always Trust**.
- **Linux (system trust):**
  ```bash
  # Debian/Ubuntu:
  sudo cp homeserver-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates
  # Fedora/RHEL:
  sudo cp homeserver-ca.crt /etc/pki/ca-trust/source/anchors/ && sudo update-ca-trust
  # Arch:
  sudo trust anchor homeserver-ca.crt
  ```
  **Firefox** (and Chrome on Linux) keep their own trust store, so also: *Settings → Privacy &
  Security → Certificates → View Certificates → Authorities → Import* → pick `homeserver-ca.crt`
  → tick *"Trust this CA to identify websites"*.

Done? [https://SERVER_IP](https://SERVER_IP) should open without a warning.

---

## 2. Passwords — Vaultwarden (Bitwarden app)

1. Open [https://SERVER_IP:8443](https://SERVER_IP:8443).
2. **Create account** → email + a strong master password. **The master password is the one thing
   nobody can reset for you — write it down somewhere safe.** (If signing up is turned off, ask
   whoever runs the server for an invitation.)
3. Phone: install **Bitwarden** (App Store / Play Store). On the login screen tap the region /
   gear → **Self-hosted** → Server URL `https://SERVER_IP:8443` → log in. (The certificate from
   step 1 must be installed for the app to connect.)

---

## 3. Files — Nextcloud

1. Open [https://SERVER_IP:8444](https://SERVER_IP:8444) and log in with the account whoever runs
   the server made for you.
2. Phone: install **Nextcloud** → server `https://SERVER_IP:8444` → log in. Turn on *Auto upload*
   if you want your photos backed up.

---

## 4. Ad blocking (optional)

If the server runs Pi-hole, a device can use it to block ads: set that device's **DNS server** to
`SERVER_IP` (Wi-Fi settings → your network → DNS → manual). Only that device is affected. If the
server is ever off, set DNS back to automatic.

---

*Everything here works on the home network only. For whoever runs the server: setup, backups and
restore are in `docs/` in the homeserver repo (`docs/setup.md`, `docs/backup-restore.md`).*
