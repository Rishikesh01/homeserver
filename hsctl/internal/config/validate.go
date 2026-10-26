package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // validate timezone names even on a box without system zoneinfo

	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
)

// ValidIPv4 checks s is a dotted IPv4 address (the certificates and binds are IPv4-only).
func ValidIPv4(s string) error {
	if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
		return fmt.Errorf("%q isn't an IPv4 address (like 192.168.1.10)", s)
	}
	return nil
}

// ValidTZ checks s is an IANA timezone name.
func ValidTZ(s string) error {
	if _, err := time.LoadLocation(s); err != nil || s == "" || s == "Local" {
		return fmt.Errorf("%q isn't a timezone name (like Europe/Brussels or Asia/Kolkata)", s)
	}
	return nil
}

// ValidPort checks s is a TCP port number.
func ValidPort(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q isn't a port number (1-65535)", s)
	}
	return nil
}

// Validate checks every setting before anything is written. A non-interactive run with a bad
// flag fails here; the prompts re-ask instead.
func Validate(c Settings) error {
	var errs []string
	add := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	add(ValidIPv4(c.ServerIP))
	add(ValidTZ(c.TZ))
	add(ValidPort(strconv.Itoa(c.UIPort)))
	if c.PiholeDNSBind != "" && c.PiholeDNSBind != c.ServerIP { // same value: already reported
		add(ValidIPv4(c.PiholeDNSBind))
	}
	for _, d := range c.DisabledApps {
		if !layout.IsApp(d) {
			errs = append(errs, fmt.Sprintf("unknown app %q in DISABLED_APPS — one of: %s", d, strings.Join(layout.Apps(), ", ")))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid settings, nothing was written:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// LocalIPv4 reports whether ip is assigned to one of this machine's interfaces.
func LocalIPv4(ip string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // can't tell — don't warn
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.String() == ip {
			return true
		}
	}
	return false
}
