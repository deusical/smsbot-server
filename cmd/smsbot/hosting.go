package main

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ValidateHosting resolves the one externally visible origin. Forwarded headers
// never select this origin. TLS certificate loading stays with the HTTP server.
func ValidateHosting(listen, origin, cert, key string) (string, error) {
	host, port, e := net.SplitHostPort(listen)
	if e != nil {
		return "", errors.New("listen must contain a literal IP address and port")
	}
	ip := net.ParseIP(host)
	number, e := strconv.Atoi(port)
	if ip == nil || e != nil || number < 1 || number > 65535 {
		return "", errors.New("listen must contain a literal IP address and valid port")
	}
	directTLS := cert != "" && key != ""
	if (cert == "") != (key == "") {
		return "", errors.New("TLS certificate and key must be supplied together")
	}
	if !ip.IsLoopback() && !directTLS {
		return "", errors.New("nonloopback listeners require direct TLS")
	}
	if origin == "" {
		if !ip.IsLoopback() {
			return "", errors.New("nonloopback listeners require an explicit HTTPS origin")
		}
		scheme := "http://"
		if directTLS {
			scheme = "https://"
		}
		origin = scheme + listen
	}
	u, e := url.Parse(origin)
	if e != nil || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("origin must be an exact HTTP(S) origin without credentials, path, query or fragment")
	}
	if directTLS && u.Scheme != "https" {
		return "", errors.New("direct TLS requires an HTTPS origin")
	}
	if !ip.IsLoopback() && u.Scheme != "https" {
		return "", errors.New("nonloopback listeners require an HTTPS origin")
	}
	if u.Scheme == "http" && u.Host != listen {
		return "", errors.New("HTTP origin must match the loopback listener")
	}
	originHost := u.Hostname()
	if originHost == "" || strings.ContainsAny(originHost, "%\\") || strings.HasSuffix(u.Host, ":") {
		return "", errors.New("invalid origin hostname")
	}
	if net.ParseIP(originHost) == nil {
		if len(originHost) > 253 {
			return "", errors.New("invalid origin hostname")
		}
		for _, label := range strings.Split(originHost, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("invalid origin hostname")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
					return "", errors.New("invalid origin hostname")
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		number, e := strconv.Atoi(port)
		if e != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid origin port")
		}
	}
	if parsed := net.ParseIP(originHost); parsed != nil && parsed.IsUnspecified() {
		return "", errors.New("origin must name a reachable host")
	}
	// Browsers canonicalize DNS case and omit default ports in Host/Origin.
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	return u.String(), nil
}
