package config

import (
	"errors"
	"net/url"
	"strings"
)

// ViewerPortalPage returns the viewer page address (ending in /ether/me) from
// Server.ViewerPortalURL, or "" when it is not set or not usable. The setting
// may name just the site (https://dashboard.example.com), a path prefix the
// proxy mounts Ether under, or the page itself.
func (s Server) ViewerPortalPage() string {
	page, err := ViewerPortalPage(s.ViewerPortalURL)
	if err != nil {
		return ""
	}
	return page
}

// ViewerPortalPage normalises a viewer page setting to its /ether/me address.
// An empty setting returns "" and no error.
func ViewerPortalPage(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("enter the full address, starting with https://")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("enter just the address, without a sign-in, ? or #")
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/ether/me")
	path = strings.TrimSuffix(path, "/ether")
	path = strings.TrimRight(path, "/")
	return u.Scheme + "://" + u.Host + path + "/ether/me", nil
}
