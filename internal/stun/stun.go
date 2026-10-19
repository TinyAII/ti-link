// Package stun 实现最小的 STUN 客户端（RFC 5389 Binding Request）。
// 只做一件事：用一个已存在的 UDP socket 探测本端在 NAT 后的公网地址。
// 必须复用打洞用的同一个 socket，否则探测到的端口与打洞端口不一致。
package stun

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

const (
	magicCookie       uint32 = 0x2112A442
	typeBindingReq    uint16 = 0x0001
	typeBindingRes    uint16 = 0x0101
	attrMappedAddr    uint16 = 0x0001
	attrXorMappedAddr uint16 = 0x0020
)

// ErrNoResponse 表示在重试次数内没收到任何有效响应。
var ErrNoResponse = errors.New("stun: 无响应")

// Discover 向 stunServer 发送 Binding Request，返回探测到的公网地址。
func Discover(conn *net.UDPConn, stunServer string, tries int) (*net.UDPAddr, error) {
	serverAddr, err := net.ResolveUDPAddr("udp4", stunServer)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 1500)
	for attempt := 0; attempt < tries; attempt++ {
		txID := make([]byte, 12)
		if _, err := rand.Read(txID); err != nil {
			return nil, err
		}

		req := make([]byte, 20)
		binary.BigEndian.PutUint16(req[0:], typeBindingReq)
		binary.BigEndian.PutUint16(req[2:], 0)
		binary.BigEndian.PutUint32(req[4:], magicCookie)
		copy(req[8:], txID)

		if _, err := conn.WriteToUDP(req, serverAddr); err != nil {
			return nil, err
		}

		conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			if addr, ok := parseResponse(buf[:n], txID); ok {
				conn.SetReadDeadline(time.Time{})
				return addr, nil
			}
		}
	}
	return nil, ErrNoResponse
}

func parseResponse(pkt, txID []byte) (*net.UDPAddr, bool) {
	if len(pkt) < 20 {
		return nil, false
	}
	if binary.BigEndian.Uint16(pkt[0:]) != typeBindingRes {
		return nil, false
	}
	if binary.BigEndian.Uint32(pkt[4:]) != magicCookie {
		return nil, false
	}
	if !bytes.Equal(pkt[8:20], txID) {
		return nil, false
	}

	end := 20 + int(binary.BigEndian.Uint16(pkt[2:]))
	if end > len(pkt) {
		end = len(pkt)
	}

	off := 20
	for off+4 <= end {
		atype := binary.BigEndian.Uint16(pkt[off:])
		alen := int(binary.BigEndian.Uint16(pkt[off+2:]))
		valStart := off + 4
		if valStart+alen > end {
			break
		}
		val := pkt[valStart : valStart+alen]
		switch atype {
		case attrXorMappedAddr:
			if a, ok := parseXorMapped(val); ok {
				return a, true
			}
		case attrMappedAddr:
			if a, ok := parseMapped(val); ok {
				return a, true
			}
		}
		off = (valStart + alen + 3) &^ 3
	}
	return nil, false
}

func parseXorMapped(v []byte) (*net.UDPAddr, bool) {
	if len(v) < 8 || v[1] != 0x01 {
		return nil, false
	}
	port := binary.BigEndian.Uint16(v[2:]) ^ uint16(magicCookie>>16)
	raw := binary.BigEndian.Uint32(v[4:8]) ^ magicCookie
	ip := net.IPv4(byte(raw>>24), byte(raw>>16), byte(raw>>8), byte(raw))
	return &net.UDPAddr{IP: ip, Port: int(port)}, true
}

func parseMapped(v []byte) (*net.UDPAddr, bool) {
	if len(v) < 8 || v[1] != 0x01 {
		return nil, false
	}
	return &net.UDPAddr{
		IP:   net.IPv4(v[4], v[5], v[6], v[7]),
		Port: int(binary.BigEndian.Uint16(v[2:])),
	}, true
}
