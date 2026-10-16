package config

import (
	"net"
	"strconv"
	"strings"
)

// InternalAPIURL reaches this daemon's listener without depending on the
// browser/auth origin or an optional frontend proxy. Explicit bind addresses
// remain usable, including IPv6-only listeners.
func (c *Config) InternalAPIURL() string {
	host := strings.Trim(c.Server.Host, "[]")
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(c.Server.Port))
}
