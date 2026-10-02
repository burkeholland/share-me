package main

import (
	"errors"

	"shareme/internal/transfer"
)

func selectNetworkIP(requested string, networks []transfer.Network) (string, error) {
	if len(networks) == 0 {
		return "", errors.New("no local network found. Connect this PC to your home network, then click Resume")
	}
	if networkAssigned(requested, networks) {
		return requested, nil
	}
	return networks[0].IP, nil
}

func networkAssigned(ip string, networks []transfer.Network) bool {
	for _, network := range networks {
		if network.IP == ip {
			return true
		}
	}
	return false
}
