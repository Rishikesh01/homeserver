package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// Flash messages: the one-line result an admin action shows on the next page ("Mounted at
// /mnt/backup", "Backup settings saved"). They used to ride in the URL (?msg=…), which meant anyone
// could send the admin a link that printed their own words inside the trusted admin UI ("Security
// update required — open the Terminal and run …"), and a refresh or bookmark replayed stale news.
// Now the message is set by the server in a short-lived cookie and shown once.

const flashCookie = "hsctl_flash"

type flash struct {
	Msg string `json:"m"`
	Err bool   `json:"e"`
}

// redirectFlash sends the browser to path, carrying msg to be shown once there (as an error when
// isErr).
func redirectFlash(w http.ResponseWriter, r *http.Request, path, msg string, isErr bool) {
	if b, err := json.Marshal(flash{Msg: msg, Err: isErr}); err == nil {
		http.SetCookie(w, &http.Cookie{
			Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString(b), Path: "/admin",
			MaxAge: 120, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
		})
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// takeFlash returns (and clears) the pending flash message, if any.
func takeFlash(w http.ResponseWriter, r *http.Request) flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return flash{}
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/admin", MaxAge: -1})
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return flash{}
	}
	var f flash
	if json.Unmarshal(b, &f) != nil {
		return flash{}
	}
	return f
}
