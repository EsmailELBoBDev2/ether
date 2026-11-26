package clientnet

import (
	"context"
	"net"
	"net/netip"
	"time"
)

// dockerDNS is Docker's embedded DNS server, present in every container on a
// user-defined network. A container that shares another's network stack
// (network_mode: service:x) behind a VPN such as Cloudflare WARP often has a
// resolv.conf pointing at the VPN's resolver instead, so container names stop
// resolving there even though Docker's server still answers.
var dockerDNS = "127.0.0.11:53"

var dockerResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: time.Second}
		return d.DialContext(ctx, network, dockerDNS)
	},
}

// LookupProxyHost resolves a proxy host or container name through the system
// resolver and, when that finds nothing, through Docker's embedded DNS.
func LookupProxyHost(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err == nil && len(addrs) > 0 {
		return addrs, nil
	}
	if docker, derr := dockerResolver.LookupNetIP(ctx, "ip", host); derr == nil && len(docker) > 0 {
		return docker, nil
	}
	return addrs, err
}

// LookupProxyAddr is LookupProxyHost's reverse: names for addr from the
// system resolver, then from Docker's embedded DNS (which names a container
// "container.network.").
func LookupProxyAddr(ctx context.Context, addr string) ([]string, error) {
	names, err := net.DefaultResolver.LookupAddr(ctx, addr)
	if err == nil && len(names) > 0 {
		return names, nil
	}
	if docker, derr := dockerResolver.LookupAddr(ctx, addr); derr == nil && len(docker) > 0 {
		return docker, nil
	}
	return names, err
}
