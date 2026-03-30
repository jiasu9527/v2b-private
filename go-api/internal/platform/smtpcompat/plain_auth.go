package smtpcompat

import (
	"errors"
	"net"
	"net/smtp"
	"strings"
)

type plainAuth struct {
	identity      string
	username      string
	password      string
	host          string
	allowInsecure bool
}

func PlainAuth(identity, username, password, host string, allowInsecure bool) smtp.Auth {
	return &plainAuth{
		identity:      identity,
		username:      username,
		password:      password,
		host:          host,
		allowInsecure: allowInsecure,
	}
}

func AllowInsecureAuth(encryption string) bool {
	switch strings.ToLower(strings.TrimSpace(encryption)) {
	case "", "none", "null", "plain":
		return true
	default:
		return false
	}
}

func (a *plainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil {
		return "", nil, errors.New("nil SMTP server info")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	if !server.TLS && !isLocalhost(server.Name) && !a.allowInsecure {
		return "", nil, errors.New("unencrypted connection")
	}
	resp := []byte(a.identity + "\x00" + a.username + "\x00" + a.password)
	return "PLAIN", resp, nil
}

func (a *plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

func isLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
