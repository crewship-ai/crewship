package skills

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Reserve an actual wrong endpoint before testing CURLOPT_RESOLVE. Darwin
// cannot generally bind an unassigned127/8 alias; try owned alternatives.
// Never dial an address/port unless its listener was successfully reserved.
// The wrong endpoint excludes normal localhost destinations127.0.0.1/::1;
// otherwise an ignored pin could accidentally satisfy the wrong control.
func gitProbeWrongEndpoint(port string) (net.IP, <-chan struct{}, func(), error) {
	candidates := []net.IP{net.ParseIP("127.0.0.2")}
	var assigned []net.IP
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, nil, err
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
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			assigned = append(assigned, ip)
			if ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				candidates = append(candidates, ip)
			}
		}
	}
	var listener net.Listener
	var selected net.IP
	for _, ip := range candidates {
		listener, err = net.Listen("tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			selected = ip
			break
		}
	}
	if listener == nil {
		return nil, nil, nil, fmt.Errorf("no owned negative Git transport endpoint available")
	}
	seen := make(chan struct{}, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			host, _, err := net.SplitHostPort(connection.RemoteAddr().String())
			remote := net.ParseIP(host)
			local := err == nil && remote != nil && remote.IsLoopback()
			if remote != nil {
				for _, ip := range assigned {
					if remote.Equal(ip) {
						local = true
						break
					}
				}
			}
			if local {
				select {
				case seen <- struct{}{}:
				default:
				}
			}
			_ = connection.Close()
		}
	}()
	var once sync.Once
	return selected, seen, func() { once.Do(func() { _ = listener.Close(); <-stopped }) }, nil
}

type gitProbeCountingListener struct {
	net.Listener
	accepts *atomic.Int64
}

func (l *gitProbeCountingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}
