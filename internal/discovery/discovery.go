// Package discovery implements UDP multicast LAN auto-discovery between the
// teacher and students.
//
// Students send an Announce to the multicast group on AnnouncePort; the
// teacher listens there and automatically registers each student (IP, MAC,
// hostname). The teacher sends a TeacherPresence on PresencePort so students
// can find it without manual configuration. The group is link-local multicast
// (224.0.0.167), so it is flooded across the classroom subnet.
package discovery

import (
	"encoding/json"
	"net"

	"tianyan/internal/protocol"
)

const (
	AnnouncePort = 5556 // student -> teacher
	PresencePort = 5557 // teacher -> student
)

const groupAddr = "224.0.0.167"

// AnnounceHandler receives student announcements.
type AnnounceHandler func(a protocol.Announce, from *net.UDPAddr)

// PresenceHandler receives teacher presence announcements.
type PresenceHandler func(p protocol.TeacherPresence, from *net.UDPAddr)

// ListenAnnounce listens for student announcements.
func ListenAnnounce(handler AnnounceHandler) (*net.UDPConn, error) {
	return listenUDP(AnnouncePort, func(data []byte, from *net.UDPAddr) {
		var a protocol.Announce
		if json.Unmarshal(data, &a) == nil {
			handler(a, from)
		}
	})
}

// ListenPresence listens for teacher presence announcements.
func ListenPresence(handler PresenceHandler) (*net.UDPConn, error) {
	return listenUDP(PresencePort, func(data []byte, from *net.UDPAddr) {
		var p protocol.TeacherPresence
		if json.Unmarshal(data, &p) == nil {
			handler(p, from)
		}
	})
}

// BroadcastAnnounce sends a student announcement to the classroom subnet.
func BroadcastAnnounce(a protocol.Announce) error {
	return broadcastJSON(AnnouncePort, a)
}

// BroadcastPresence sends a teacher presence announcement to the classroom subnet.
func BroadcastPresence(p protocol.TeacherPresence) error {
	return broadcastJSON(PresencePort, p)
}

func listenUDP(port int, handler func([]byte, *net.UDPAddr)) (*net.UDPConn, error) {
	c, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(groupAddr), Port: port})
	if err != nil {
		return nil, err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			handler(data, addr)
		}
	}()
	return c, nil
}

func broadcastJSON(port int, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(groupAddr), Port: port})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(data)
	return err
}
