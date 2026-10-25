package server

import (
	"net"
	"net/url"
	"strconv"
)

func parseLoose(raw string) (*url.URL, error) { return url.Parse(raw) }

func hostPort(host string, port int) string {
	if port == 80 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
