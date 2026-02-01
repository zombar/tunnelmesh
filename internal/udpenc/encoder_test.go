package udpenc

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncoder_WriteFrame(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := NewEncoder(buf)

	data := []byte("hello world")
	n, err := enc.WriteFrame(ProtoUDP, data)
	require.NoError(t, err)
	assert.Equal(t, len(data)+3, n) // 2 bytes length + 1 byte proto + data

	// Check header
	result := buf.Bytes()
	assert.Equal(t, byte(0), result[0])           // Length high byte
	assert.Equal(t, byte(len(data)+1), result[1]) // Length low byte (data + proto)
	assert.Equal(t, byte(ProtoUDP), result[2])    // Protocol
	assert.Equal(t, data, result[3:])             // Data
}

func TestDecoder_ReadFrame(t *testing.T) {
	// Prepare encoded data
	data := []byte("hello world")
	encoded := make([]byte, len(data)+3)
	encoded[0] = 0                   // Length high
	encoded[1] = byte(len(data) + 1) // Length low (data + proto)
	encoded[2] = byte(ProtoUDP)      // Protocol
	copy(encoded[3:], data)

	dec := NewDecoder(bytes.NewReader(encoded))

	proto, payload, err := dec.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, ProtoUDP, proto)
	assert.Equal(t, data, payload)
}

func TestEncoderDecoder_Roundtrip(t *testing.T) {
	tests := []struct {
		name  string
		proto Protocol
		data  []byte
	}{
		{"small UDP", ProtoUDP, []byte("hi")},
		{"medium TCP", ProtoTCP, []byte("hello world this is a test message")},
		{"empty", ProtoUDP, []byte{}},
		{"binary data", ProtoTCP, []byte{0x00, 0x01, 0xff, 0xfe, 0x7f}},
		{"max size", ProtoUDP, bytes.Repeat([]byte("x"), MaxPayloadSize)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}

			// Encode
			enc := NewEncoder(buf)
			_, err := enc.WriteFrame(tt.proto, tt.data)
			require.NoError(t, err)

			// Decode
			dec := NewDecoder(buf)
			proto, payload, err := dec.ReadFrame()
			require.NoError(t, err)

			assert.Equal(t, tt.proto, proto)
			assert.Equal(t, tt.data, payload)
		})
	}
}

func TestDecoder_MultipleFrames(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := NewEncoder(buf)

	// Write multiple frames
	frames := []struct {
		proto Protocol
		data  []byte
	}{
		{ProtoUDP, []byte("frame1")},
		{ProtoTCP, []byte("frame2")},
		{ProtoUDP, []byte("frame3")},
	}

	for _, f := range frames {
		_, err := enc.WriteFrame(f.proto, f.data)
		require.NoError(t, err)
	}

	// Read them back
	dec := NewDecoder(buf)
	for i, expected := range frames {
		proto, payload, err := dec.ReadFrame()
		require.NoError(t, err, "frame %d", i)
		assert.Equal(t, expected.proto, proto, "frame %d proto", i)
		assert.Equal(t, expected.data, payload, "frame %d data", i)
	}
}

func TestDecoder_EOF(t *testing.T) {
	dec := NewDecoder(bytes.NewReader(nil))

	_, _, err := dec.ReadFrame()
	assert.ErrorIs(t, err, io.EOF)
}

func TestEncoder_OversizedPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := NewEncoder(buf)

	// Try to write data larger than MaxPayloadSize
	data := bytes.Repeat([]byte("x"), MaxPayloadSize+1)
	_, err := enc.WriteFrame(ProtoUDP, data)
	assert.Error(t, err)
}

func TestDecoder_TruncatedHeader(t *testing.T) {
	// Only 1 byte of header
	dec := NewDecoder(bytes.NewReader([]byte{0x00}))

	_, _, err := dec.ReadFrame()
	assert.Error(t, err)
}

func TestDecoder_TruncatedPayload(t *testing.T) {
	// Header says 10 bytes but only 5 present
	data := []byte{0x00, 0x0a, byte(ProtoUDP), 0x01, 0x02, 0x03, 0x04, 0x05}
	dec := NewDecoder(bytes.NewReader(data))

	_, _, err := dec.ReadFrame()
	assert.Error(t, err)
}

func TestFrameReader(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := NewEncoder(buf)

	testData := []byte("test payload data")
	_, err := enc.WriteFrame(ProtoUDP, testData)
	require.NoError(t, err)

	reader := NewFrameReader(buf)
	result := make([]byte, 100)
	n, err := reader.Read(result)
	require.NoError(t, err)

	// Result includes protocol byte
	assert.Equal(t, byte(ProtoUDP), result[0])
	assert.Equal(t, testData, result[1:n])
}

func TestFrameWriter(t *testing.T) {
	buf := &bytes.Buffer{}
	writer := NewFrameWriter(buf, ProtoUDP)

	testData := []byte("test payload data")
	n, err := writer.Write(testData)
	require.NoError(t, err)
	assert.Equal(t, len(testData), n)

	// Read it back
	dec := NewDecoder(buf)
	proto, payload, err := dec.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, ProtoUDP, proto)
	assert.Equal(t, testData, payload)
}
