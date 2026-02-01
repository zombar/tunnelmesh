// Package udpenc implements UDP-over-TCP encapsulation for tunnelmesh.
// It provides a simple framing protocol to send UDP datagrams over TCP streams.
//
// Frame format:
//
//	[2 bytes: payload length (big-endian)] [1 byte: protocol] [payload]
//
// This preserves packet boundaries when sending over TCP.
package udpenc

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Protocol identifies the encapsulated protocol type.
type Protocol byte

const (
	// ProtoUDP indicates a UDP packet.
	ProtoUDP Protocol = 0x01
	// ProtoTCP indicates a TCP packet (for future use).
	ProtoTCP Protocol = 0x02
)

const (
	// HeaderSize is the size of the frame header (length + protocol).
	HeaderSize = 3
	// MaxPayloadSize is the maximum payload size (64KB - header).
	MaxPayloadSize = 65535 - HeaderSize
)

// Encoder writes framed data to an underlying writer.
type Encoder struct {
	w  io.Writer
	mu sync.Mutex
}

// NewEncoder creates a new encoder.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// WriteFrame writes a single frame with the given protocol and payload.
func (e *Encoder) WriteFrame(proto Protocol, payload []byte) (int, error) {
	if len(payload) > MaxPayloadSize {
		return 0, fmt.Errorf("payload too large: %d > %d", len(payload), MaxPayloadSize)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Build frame: [length (2)][proto (1)][payload]
	frameLen := len(payload) + 1 // +1 for protocol byte
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint16(header[0:2], uint16(frameLen))
	header[2] = byte(proto)

	// Write header
	if _, err := e.w.Write(header); err != nil {
		return 0, fmt.Errorf("write header: %w", err)
	}

	// Write payload
	if len(payload) > 0 {
		if _, err := e.w.Write(payload); err != nil {
			return 0, fmt.Errorf("write payload: %w", err)
		}
	}

	return HeaderSize + len(payload), nil
}

// Decoder reads framed data from an underlying reader.
type Decoder struct {
	r  io.Reader
	mu sync.Mutex
}

// NewDecoder creates a new decoder.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// ReadFrame reads a single frame and returns the protocol and payload.
func (d *Decoder) ReadFrame() (Protocol, []byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Read header
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(d.r, header); err != nil {
		return 0, nil, err
	}

	frameLen := binary.BigEndian.Uint16(header[0:2])
	proto := Protocol(header[2])

	// Read payload (frameLen includes protocol byte, which we already read)
	payloadLen := int(frameLen) - 1
	if payloadLen < 0 {
		return 0, nil, fmt.Errorf("invalid frame length: %d", frameLen)
	}

	if payloadLen == 0 {
		return proto, []byte{}, nil
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(d.r, payload); err != nil {
		return 0, nil, fmt.Errorf("read payload: %w", err)
	}

	return proto, payload, nil
}

// FrameReader wraps a reader and provides frame-based reading.
// Each Read returns exactly one frame's worth of data.
type FrameReader struct {
	dec *Decoder
}

// NewFrameReader creates a new frame reader.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{dec: NewDecoder(r)}
}

// Read reads the next frame into p. The first byte is the protocol,
// followed by the payload.
func (fr *FrameReader) Read(p []byte) (int, error) {
	proto, payload, err := fr.dec.ReadFrame()
	if err != nil {
		return 0, err
	}

	needed := 1 + len(payload)
	if len(p) < needed {
		return 0, fmt.Errorf("buffer too small: %d < %d", len(p), needed)
	}

	p[0] = byte(proto)
	copy(p[1:], payload)
	return needed, nil
}

// FrameWriter wraps a writer and provides frame-based writing.
// Each Write is sent as a single frame.
type FrameWriter struct {
	enc   *Encoder
	proto Protocol
}

// NewFrameWriter creates a new frame writer with a fixed protocol.
func NewFrameWriter(w io.Writer, proto Protocol) *FrameWriter {
	return &FrameWriter{
		enc:   NewEncoder(w),
		proto: proto,
	}
}

// Write writes p as a single frame.
func (fw *FrameWriter) Write(p []byte) (int, error) {
	_, err := fw.enc.WriteFrame(fw.proto, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
