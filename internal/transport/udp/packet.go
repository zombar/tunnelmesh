// Package udp implements an encrypted UDP transport for tunnelmesh.
package udp

import (
	"encoding/binary"
	"fmt"
)

// Packet types
const (
	PacketTypeHandshakeInit     = 0x01
	PacketTypeHandshakeResponse = 0x02
	PacketTypeData              = 0x04
	PacketTypeKeepalive         = 0x05
	PacketTypeRekeyRequired     = 0x06 // Sent when session not found, tells peer to re-handshake
)

// Packet sizes
const (
	HeaderSize       = 16 // type(1) + reserved(3) + receiver(4) + counter(8)
	AuthTagSize      = 16 // Poly1305 tag
	MaxPayloadSize   = 1500 - 20 - 8 - HeaderSize - AuthTagSize // MTU - IP - UDP - header - tag
	MinPacketSize    = HeaderSize + AuthTagSize
	HandshakeSize    = 148 // Noise IK handshake message size
)

// PacketHeader is the unencrypted header of every UDP packet.
// Format: [1 type][3 reserved][4 receiver][8 counter]
type PacketHeader struct {
	Type     uint8
	Receiver uint32 // Receiver's session index
	Counter  uint64 // Nonce/sequence number
}

// Marshal serializes the header to bytes.
func (h *PacketHeader) Marshal() []byte {
	buf := make([]byte, HeaderSize)
	buf[0] = h.Type
	// bytes 1-3 reserved (zero)
	binary.BigEndian.PutUint32(buf[4:8], h.Receiver)
	binary.BigEndian.PutUint64(buf[8:16], h.Counter)
	return buf
}

// UnmarshalHeader parses a packet header from bytes.
func UnmarshalHeader(data []byte) (*PacketHeader, error) {
	if len(data) < HeaderSize {
		return nil, fmt.Errorf("packet too short: %d < %d", len(data), HeaderSize)
	}

	return &PacketHeader{
		Type:     data[0],
		Receiver: binary.BigEndian.Uint32(data[4:8]),
		Counter:  binary.BigEndian.Uint64(data[8:16]),
	}, nil
}

// DataPacket represents an encrypted data packet.
type DataPacket struct {
	Header     PacketHeader
	Ciphertext []byte // Encrypted payload + auth tag
}

// Marshal serializes the data packet.
func (p *DataPacket) Marshal() []byte {
	header := p.Header.Marshal()
	result := make([]byte, len(header)+len(p.Ciphertext))
	copy(result, header)
	copy(result[len(header):], p.Ciphertext)
	return result
}

// UnmarshalDataPacket parses a data packet from bytes.
func UnmarshalDataPacket(data []byte) (*DataPacket, error) {
	if len(data) < MinPacketSize {
		return nil, fmt.Errorf("packet too short: %d < %d", len(data), MinPacketSize)
	}

	header, err := UnmarshalHeader(data)
	if err != nil {
		return nil, err
	}

	if header.Type != PacketTypeData && header.Type != PacketTypeKeepalive {
		return nil, fmt.Errorf("unexpected packet type: %d", header.Type)
	}

	return &DataPacket{
		Header:     *header,
		Ciphertext: data[HeaderSize:],
	}, nil
}

// HandshakeInitPacket is the first message in the Noise IK handshake.
// Contains: static public key (encrypted), ephemeral public key, payload
type HandshakeInitPacket struct {
	Header           PacketHeader
	SenderIndex      uint32   // Sender's chosen session index
	EphemeralPublic  [32]byte // e
	EncryptedStatic  [48]byte // AEAD(s) - static public key encrypted
	EncryptedPayload []byte   // AEAD(timestamp + padding)
}

// HandshakeResponsePacket is the response in the Noise IK handshake.
type HandshakeResponsePacket struct {
	Header          PacketHeader
	SenderIndex     uint32   // Responder's chosen session index
	ReceiverIndex   uint32   // Initiator's session index (from init)
	EphemeralPublic [32]byte // e
	EncryptedEmpty  [16]byte // AEAD() - empty payload with auth tag
}

// KeepalivePacket is sent to maintain NAT mappings.
type KeepalivePacket struct {
	Header PacketHeader
}

// NewKeepalivePacket creates a keepalive packet.
func NewKeepalivePacket(receiver uint32) *KeepalivePacket {
	return &KeepalivePacket{
		Header: PacketHeader{
			Type:     PacketTypeKeepalive,
			Receiver: receiver,
			Counter:  0, // Keepalives don't use counter
		},
	}
}

// RekeyRequiredPacket is sent when we receive data for an unknown session.
// It tells the sender that their session is stale and they need to re-handshake.
// Format: [1 type][4 unknown_receiver_index]
type RekeyRequiredPacket struct {
	UnknownIndex uint32 // The receiver index we received but don't recognize
}

// RekeyRequiredSize is the size of a rekey-required packet.
const RekeyRequiredSize = 5

// NewRekeyRequiredPacket creates a rekey-required packet.
func NewRekeyRequiredPacket(unknownIndex uint32) *RekeyRequiredPacket {
	return &RekeyRequiredPacket{
		UnknownIndex: unknownIndex,
	}
}

// Marshal serializes the rekey-required packet.
func (p *RekeyRequiredPacket) Marshal() []byte {
	buf := make([]byte, RekeyRequiredSize)
	buf[0] = PacketTypeRekeyRequired
	binary.BigEndian.PutUint32(buf[1:5], p.UnknownIndex)
	return buf
}

// UnmarshalRekeyRequired parses a rekey-required packet from bytes.
func UnmarshalRekeyRequired(data []byte) (*RekeyRequiredPacket, error) {
	if len(data) < RekeyRequiredSize {
		return nil, fmt.Errorf("rekey packet too short: %d < %d", len(data), RekeyRequiredSize)
	}
	return &RekeyRequiredPacket{
		UnknownIndex: binary.BigEndian.Uint32(data[1:5]),
	}, nil
}
