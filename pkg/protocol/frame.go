// Package protocol implements the GT06/Concox binary framing protocol.
//
// Short packet wire format:
//
//	0x78 0x78 | Len(1) | Proto(1) | Body(N) | Serial(2) | CRC16(2) | 0x0D 0x0A
//
// Long packet wire format (body ≥ 251 bytes):
//
//	0x79 0x79 | Len(2) | Proto(1) | Body(N) | Serial(2) | CRC16(2) | 0x0D 0x0A
//
// Len counts: Proto(1) + Body(N) + Serial(2) + CRC16(2).
// CRC16 covers: Proto + Body + Serial  (all bytes that Len counts, except the 2 CRC bytes).
// CRC algorithm: CRC-16/IBM — poly 0x8005, init 0x0000, RefIn=true, RefOut=true (reflected form).
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Frame is a fully decoded GT06 frame with checksum validated.
type Frame struct {
	Protocol uint8
	Body     []byte
	Serial   uint16
	IsLong   bool // true when decoded from a 0x79 0x79 long packet
}

// ReadFrame reads one complete GT06 frame from r.
// It scans past garbage bytes until a valid 0x78 0x78 or 0x79 0x79 start pair is found.
// Returns io.EOF if the connection is cleanly closed before any frame byte arrives.
func ReadFrame(r io.Reader) (*Frame, error) {
	var one [1]byte
	for {
		if _, err := io.ReadFull(r, one[:]); err != nil {
			return nil, err // io.EOF propagates naturally
		}
		b := one[0]
		if b != 0x78 && b != 0x79 {
			continue
		}
		first := b
		if _, err := io.ReadFull(r, one[:]); err != nil {
			return nil, fmt.Errorf("gt06: second start byte: %w", err)
		}
		if one[0] != first {
			continue // 0x78 0x79 or similar mismatch — keep scanning
		}
		return readBody(r, first == 0x79)
	}
}

func readBody(r io.Reader, isLong bool) (*Frame, error) {
	var length int
	if isLong {
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, fmt.Errorf("gt06: length(2) read: %w", err)
		}
		length = int(binary.BigEndian.Uint16(b[:]))
	} else {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, fmt.Errorf("gt06: length(1) read: %w", err)
		}
		length = int(b[0])
	}

	// Len = proto(1) + body + serial(2) + crc(2); minimum 5 (empty body)
	if length < 5 {
		return nil, fmt.Errorf("gt06: length %d too short (min 5)", length)
	}
	if length > 1024 {
		return nil, fmt.Errorf("gt06: length %d exceeds max 1024", length)
	}

	// Read proto + body + serial + crc in one shot.
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("gt06: payload read: %w", err)
	}

	// Consume trailing 0x0D 0x0A (CRLF).
	var crlf [2]byte
	if _, err := io.ReadFull(r, crlf[:]); err != nil {
		return nil, fmt.Errorf("gt06: CRLF read: %w", err)
	}

	// Validate CRC: covers buf[0 : length-2] (proto + body + serial).
	wantCRC := binary.BigEndian.Uint16(buf[length-2:])
	if gotCRC := CRC16(buf[:length-2]); gotCRC != wantCRC {
		return nil, fmt.Errorf("gt06: CRC mismatch: got 0x%04X want 0x%04X", gotCRC, wantCRC)
	}

	proto := buf[0]
	bodyLen := length - 5 // proto(1) + serial(2) + crc(2)
	body := make([]byte, bodyLen)
	copy(body, buf[1:1+bodyLen])
	serial := binary.BigEndian.Uint16(buf[1+bodyLen : 1+bodyLen+2])

	return &Frame{Protocol: proto, Body: body, Serial: serial, IsLong: isLong}, nil
}

// WriteACK sends a minimal server response for the given protocol code and device serial.
//
//	Wire: 0x78 0x78 | 0x05 | proto | serial(2) | crc(2) | 0x0D 0x0A
//
// The CRC covers proto + serial (3 bytes), matching how ReadFrame validates incoming frames.
func WriteACK(w io.Writer, proto uint8, serial uint16) error {
	crcData := []byte{proto, byte(serial >> 8), byte(serial)}
	crc := CRC16(crcData)
	pkt := []byte{
		0x78, 0x78,
		0x05,
		proto,
		byte(serial >> 8), byte(serial),
		byte(crc >> 8), byte(crc),
		0x0D, 0x0A,
	}
	_, err := w.Write(pkt)
	return err
}

// CRC16 computes CRC-16/IBM over data.
// Parameters: poly=0x8005, init=0x0000, RefIn=true, RefOut=true, XorOut=0x0000.
// This is the reflected form, equivalent to using the bit-reversed polynomial 0xA001.
func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
