package peer

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
)

func (e *Engine) filterOffer(ctx context.Context, sdp string) (string, error) {
	// Resolve browser-obfuscated host candidates before Pion sees them. Pion's
	// public selected-candidate API retains .local names instead of the resolved
	// IP, which is insufficient for native approval and HTTP RemoteAddr.
	var resolver *mdns.Conn
	defer func() {
		if resolver != nil {
			_ = resolver.Close()
		}
	}()
	resolved := map[string]net.IP{}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	lines := strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n")
	if len(lines) > 256 {
		return "", errors.New("too many SDP lines")
	}
	result := make([]string, 0, len(lines))
	candidates, permitted, media := 0, 0, 0
	for _, line := range lines {
		if len(line) > 2048 {
			return "", errors.New("SDP line too long")
		}
		if strings.HasPrefix(line, "m=") {
			media++
			if !strings.HasPrefix(line, "m=application ") || media > 1 {
				return "", errors.New("only one application transport is permitted")
			}
		}
		if !strings.HasPrefix(line, "a=candidate:") {
			if line != "" {
				result = append(result, line)
			}
			continue
		}
		candidates++
		if candidates > 64 {
			return "", errors.New("too many ICE candidates")
		}
		candidate, err := ice.UnmarshalCandidate(strings.TrimPrefix(line, "a="))
		if err != nil {
			return "", errors.New("invalid ICE candidate")
		}
		if candidate.Type() != ice.CandidateTypeHost || candidate.Component() != 1 || candidate.NetworkType() != ice.NetworkTypeUDP4 {
			continue
		}
		address := candidate.Address()
		ip := net.ParseIP(address)
		if strings.HasSuffix(address, ".local") {
			if len(address) > 253 || strings.ContainsAny(address, " /\r\n\t") {
				return "", errors.New("invalid mDNS candidate")
			}
			var cached bool
			ip, cached = resolved[address]
			if !cached {
				if len(resolved) >= 8 {
					return "", errors.New("too many mDNS candidates")
				}
				if resolver == nil {
					conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
					if err != nil {
						return "", err
					}
					resolver, err = mdns.Server(ipv4.NewPacketConn(conn), nil, &mdns.Config{
						Interfaces: []net.Interface{*e.iface}, IncludeLoopback: e.cfg.AllowLoopback,
					})
					if err != nil {
						_ = conn.Close()
						return "", err
					}
				}
				_, found, err := resolver.QueryAddr(ctx, address)
				if err != nil {
					return "", errors.New("browser LAN address could not be resolved")
				}
				ip = net.IP(found.AsSlice())
				resolved[address] = ip
			}
		}
		if !e.permittedRemote(ip) || candidate.Port() <= 0 || candidate.Port() > 65535 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return "", errors.New("invalid candidate fields")
		}
		fields[4] = ip.String()
		result = append(result, strings.Join(fields, " "))
		permitted++
	}
	if media != 1 || permitted == 0 {
		return "", errors.New("offer has no permitted private LAN candidates")
	}
	return strings.Join(result, "\r\n") + "\r\n", nil
}
