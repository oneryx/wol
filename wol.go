package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ParseMAC accepts 01:23:45:67:89:ab, 01-23-45-67-89-ab, 0123.4567.89ab or
// 0123456789ab and returns it normalised to upper case with colons.
func ParseMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) == 12 && !strings.ContainsAny(s, ":-.") {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(s[i : i+2])
		}
		s = b.String()
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid MAC address %q", s)
	}
	return strings.ToUpper(hw.String()), nil
}

// MagicPacket is 6 bytes of 0xFF followed by the MAC repeated 16 times.
func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("invalid MAC address %q", mac)
	}
	p := make([]byte, 0, 102)
	for range 6 {
		p = append(p, 0xFF)
	}
	for range 16 {
		p = append(p, hw...)
	}
	return p, nil
}

// Wake sends a magic packet for mac to broadcast:port over UDP. Go enables
// SO_BROADCAST on UDP sockets. The socket is left unconnected because macOS
// refuses writes to 255.255.255.255 on a connected one (EPIPE).
func Wake(mac, broadcast string, port int) error {
	packet, err := MagicPacket(mac)
	if err != nil {
		return err
	}
	ip := net.ParseIP(broadcast)
	if ip == nil {
		return fmt.Errorf("invalid broadcast address %q", broadcast)
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.WriteToUDP(packet, &net.UDPAddr{IP: ip, Port: port})
	return err
}

// probePorts are tried when a device's host has no explicit port. Any answer
// counts, including a refused connection.
var probePorts = []string{"22", "80", "443", "445", "3389", "8080"}

const probeTimeout = 1500 * time.Millisecond

// Online reports whether host ("name", "ip" or "ip:port") answers on TCP.
// A connection refused (RST) still proves the machine is up, so this works
// without root (no ICMP) even when nothing listens on the probed ports. Hosts
// that silently drop all probed ports show as offline.
func Online(ctx context.Context, host string) bool {
	targets := []string{host}
	if _, _, err := net.SplitHostPort(host); err != nil {
		targets = targets[:0]
		for _, p := range probePorts {
			targets = append(targets, net.JoinHostPort(host, p))
		}
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	found := make(chan struct{}, len(targets))
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Go(func() {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", t)
			if err == nil {
				conn.Close()
				found <- struct{}{}
			} else if errors.Is(err, syscall.ECONNREFUSED) {
				found <- struct{}{}
			}
		})
	}
	go func() { wg.Wait(); close(found) }()
	_, ok := <-found
	return ok
}
