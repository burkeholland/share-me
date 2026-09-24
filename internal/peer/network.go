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
	var resolver *mdns.Conn
	defer func() {
		if resolver != nil {
			_ = resolver.Close()
		}
	}()
	resolved := map[string]net.IP{}
	mdnsNames := map[string]struct{}{}
	mdnsUnavailable := false
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	lines := strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n")
	if len(lines) > 256 {
		return "", errors.New("too many SDP lines")
	}
	result := make([]string, 0, len(lines))
	candidates, usable, media := 0, 0, 0
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
		if candidate.Port() <= 0 || candidate.Port() > 65535 {
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
				if _, seen := mdnsNames[address]; !seen {
					if len(mdnsNames) >= 8 {
						return "", errors.New("too many mDNS candidates")
					}
					mdnsNames[address] = struct{}{}
				}
				if resolver == nil && !mdnsUnavailable {
					mdnsAddress, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
					if err == nil {
						var conn *net.UDPConn
						conn, err = net.ListenUDP("udp4", mdnsAddress)
						if err == nil {
							resolver, err = mdns.Server(ipv4.NewPacketConn(conn), nil, &mdns.Config{
								Name: "shareme-ice", Interfaces: []net.Interface{*e.iface},
								IncludeLoopback: e.cfg.AllowLoopback,
							})
							if err != nil {
								_ = conn.Close()
							}
						}
					}
					if err != nil {
						mdnsUnavailable = true
					}
				}
				if resolver != nil {
					_, found, err := resolver.QueryAddr(ctx, address)
					if err == nil {
						ip = net.IP(found.AsSlice())
						resolved[address] = ip
						cached = true
					}
				}
				if !cached {
					// The controlling browser may still establish a
					// peer-reflexive path by checking our answer directly.
					usable++
					continue
				}
			}
		}
		if !e.permittedRemote(ip) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return "", errors.New("invalid candidate fields")
		}
		fields[4] = ip.String()
		result = append(result, strings.Join(fields, " "))
		usable++
	}
	if media != 1 || usable == 0 {
		return "", errors.New("offer has no permitted private LAN candidates")
	}
	return strings.Join(result, "\r\n") + "\r\n", nil
}
