package shortcut

import (
	"errors"
	"net"
)

func selectedNetwork(local string, allowLoopback bool) (net.IP, *net.IPNet, error) {
	ip := net.ParseIP(local).To4()
	if ip == nil || !(ip.IsPrivate() || allowLoopback && ip.IsLoopback()) {
		return nil, nil, errors.New("shortcut requires a selected private IPv4 interface")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if subnet, ok := address.(*net.IPNet); ok && subnet.IP.Equal(ip) {
				return ip, &net.IPNet{IP: ip.Mask(subnet.Mask), Mask: subnet.Mask}, nil
			}
		}
	}
	return nil, nil, errors.New("shortcut address is not on an active interface")
}

func (s *Server) permittedRemote(address net.Addr) bool {
	remote, ok := address.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip := remote.IP.To4()
	if ip == nil || !s.subnet.Contains(ip) || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsLoopback() {
		return s.cfg.AllowLoopback && s.ip.IsLoopback()
	}
	if !ip.IsPrivate() || s.ip.IsLoopback() {
		return false
	}
	network, broadcast := true, true
	for i, b := range ip {
		network = network && b&^s.subnet.Mask[i] == 0
		broadcast = broadcast && b|s.subnet.Mask[i] == 255
	}
	return !network && !broadcast
}
