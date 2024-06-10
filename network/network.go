package network

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

type IFace string
type CIDR string

// Looks through all network interfaces and returns the names of those that have a private IP4 address
func GetPrivateIP4Interfaces() (map[IFace]CIDR, error) {
	found := make(map[IFace]CIDR)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("error getting interfaces: %w", err)
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("error getting address for interface %q: %w", iface.Name, err)
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil && ipNet.IP.IsPrivate() {
				mask := net.IPv4Mask(ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
				bits, _ := mask.Size()
				found[IFace(iface.Name)] = CIDR(fmt.Sprintf("%s/%d", ipNet.IP.String(), bits))
				break
			}
		}
	}
	return found, nil
}

// returns all the IP addresses on the network
func GetIPsOnNetwork(cidr CIDR) ([]string, error) {
	p, err := netip.ParsePrefix(string(cidr))
	if err != nil {
		return nil, fmt.Errorf("invalid cidr: %q: %w", cidr, err)
	}
	// 8.8.8.8/24 => 8.8.8.0/24
	p = p.Masked()

	ips := []string{}
	addr := p.Addr()
	for {
		if !p.Contains(addr) {
			break
		}
		ips = append(ips, addr.String())
		addr = addr.Next()
	}
	return ips, nil
}

// checks if a port is open on the given target
func IsPortOpen(address string, timeout time.Duration) (bool, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)

	if err != nil {
		if strings.Contains(err.Error(), "too many open files") {
			return false, fmt.Errorf("error opening %q: %w", address, err)
		}
		return false, nil
	}

	conn.Close()
	return true, nil
}
