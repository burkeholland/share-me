package transfer

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

type Network struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// Networks returns usable RFC1918 IPv4 addresses, not an automatic bind choice.
// Virtual-interface ordering is a best-effort name heuristic.
func Networks() ([]Network, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	result := make([]Network, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("list addresses for %s: %w", iface.Name, err)
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !ip.IsPrivate() {
				continue
			}
			result = append(result, Network{Name: iface.Name, IP: ip.String()})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if virtualInterface(a.Name) != virtualInterface(b.Name) {
			return !virtualInterface(a.Name)
		}
		if strings.ToLower(a.Name) != strings.ToLower(b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return netip.MustParseAddr(a.IP).Less(netip.MustParseAddr(b.IP))
	})
	return result, nil
}

func virtualInterface(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range []string{"virtual", "vethernet", "hyper-v", "vmware", "vbox", "docker", "wsl", "vpn", "tailscale", "zerotier", "tun", "tap"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func validateBindIP(value string, allowLoopback bool) (string, error) {
	ip, err := netip.ParseAddr(value)
	if err != nil || !ip.Is4() {
		return "", fmt.Errorf("bind address must be a local RFC1918 IPv4 address")
	}
	if ip == netip.MustParseAddr("127.0.0.1") && allowLoopback {
		return ip.String(), nil
	}
	if !ip.IsPrivate() {
		return "", fmt.Errorf("bind address must be a local RFC1918 IPv4 address")
	}
	networks, err := Networks()
	if err != nil {
		return "", err
	}
	for _, network := range networks {
		if network.IP == ip.String() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("bind address is not assigned to an active local interface")
}
