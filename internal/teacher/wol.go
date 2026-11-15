package teacher

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// wakeOnLAN sends a Wake-on-LAN magic packet to the broadcast address on
// port 9 for the given MAC address.
func wakeOnLAN(mac string) error {
	mac = strings.NewReplacer(":", "", "-", "", " ", "").Replace(mac)
	raw, err := hex.DecodeString(mac)
	if err != nil || len(raw) != 6 {
		return fmt.Errorf("invalid MAC address %q", mac)
	}
	pkt := make([]byte, 102)
	for i := 0; i < 6; i++ {
		pkt[i] = 0xFF
	}
	for i := 0; i < 16; i++ {
		copy(pkt[6+i*6:], raw)
	}
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := setUDPBroadcast(c); err != nil {
		return err
	}
	_, err = c.Write(pkt)
	return err
}
