package concerns

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"unicode/utf8"
)

const (
	MaxEventStreamMessageBytes = 24 << 20
	MaxEventStreamHeadersBytes = 128 << 10
)

// EventStreamTimestamp is header kind 8: milliseconds since the Unix epoch. It is a distinct type so
// a timestamp never reads as a kind 5 long (int64).
type EventStreamTimestamp int64

// EventStreamEvent retains typed headers and opaque payload bytes; protocol adapters own JSON and event semantics.
// Header values by kind: 0/1 bool, 2 int8, 3 int16, 4 int32, 5 int64, 6 []byte, 7 string,
// 8 EventStreamTimestamp, 9 [16]byte.
// Payload and kind 6 header values are views into the frame buffer, which is allocated per frame and never
// reused by the reader; they stay valid and unmodified, but retaining one keeps the whole frame (up to
// MaxEventStreamMessageBytes) alive. Copy what must outlive the event.
type EventStreamEvent struct {
	Headers map[string]any
	Payload []byte
}

// TruncatedFrameError is a stream that ended inside a frame; Pending is the bytes
// of that frame already read. It is io.ErrUnexpectedEOF to errors.Is.
type TruncatedFrameError struct{ Pending int }

func (e *TruncatedFrameError) Error() string { return io.ErrUnexpectedEOF.Error() }
func (e *TruncatedFrameError) Unwrap() error { return io.ErrUnexpectedEOF }

// NoTerminalError is a stream that ended cleanly, between frames, without any
// consumer marking a terminal. It is io.ErrUnexpectedEOF to errors.Is; a provider
// whose protocol completes such a stream (Kiro) tells it from a cut stream by type.
type NoTerminalError struct{}

func (e *NoTerminalError) Error() string { return io.ErrUnexpectedEOF.Error() }
func (e *NoTerminalError) Unwrap() error { return io.ErrUnexpectedEOF }

// ReadEventStream validates frames before delivery. consume marks a terminal; success requires it and clean EOF.
// It reads past terminals to detect trailing corruption. Callers emit success only after nil and close src on cancellation.
func ReadEventStream(ctx context.Context, src io.Reader, consume func(EventStreamEvent) (bool, error)) error {
	if src == nil || consume == nil {
		return errors.New("eventstream reader and consumer are required")
	}
	read := func(dst []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := io.ReadFull(src, dst)
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, err
	}
	terminal, total := false, 0
	for {
		var prelude [12]byte
		n, err := read(prelude[:])
		if err != nil {
			if err == io.EOF {
				if terminal {
					return nil
				}
				return &NoTerminalError{}
			}
			if err == io.ErrUnexpectedEOF {
				return &TruncatedFrameError{Pending: n}
			}
			return err
		}
		total += n
		if binary.BigEndian.Uint32(prelude[8:]) != crc32.ChecksumIEEE(prelude[:8]) {
			return errors.New("eventstream prelude CRC mismatch")
		}
		length := binary.BigEndian.Uint32(prelude[:4])
		headersLength := binary.BigEndian.Uint32(prelude[4:8])
		if length < 16 || length > MaxEventStreamMessageBytes || headersLength > MaxEventStreamHeadersBytes || headersLength > length-16 {
			return errors.New("eventstream frame bounds are invalid")
		}
		if int(length)-12 > MaxTranslatedResponseBytes-total {
			return errors.New("translated upstream response exceeds limit")
		}
		frame := make([]byte, int(length))
		copy(frame, prelude[:])
		if got, err := read(frame[12:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return &TruncatedFrameError{Pending: 12 + got}
			}
			return err
		}
		total += len(frame) - 12
		if binary.BigEndian.Uint32(frame[len(frame)-4:]) != crc32.ChecksumIEEE(frame[:len(frame)-4]) {
			return errors.New("eventstream message CRC mismatch")
		}
		headers, err := eventStreamHeaders(frame[12 : 12+int(headersLength)])
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		last, err := consume(EventStreamEvent{Headers: headers, Payload: frame[12+int(headersLength) : len(frame)-4]})
		if err != nil {
			return err
		}
		terminal = terminal || last
	}
}

func eventStreamHeaders(raw []byte) (map[string]any, error) {
	headers := map[string]any{}
	for len(raw) > 0 {
		nameLength := int(raw[0])
		if nameLength == 0 || len(raw) < nameLength+2 || !utf8.Valid(raw[1:1+nameLength]) {
			return nil, errors.New("invalid EventStream header name")
		}
		name := string(raw[1 : 1+nameLength])
		if _, exists := headers[name]; exists {
			return nil, errors.New("duplicate EventStream header")
		}
		kind := raw[1+nameLength]
		raw = raw[nameLength+2:]
		var value any
		size := 0
		switch kind {
		case 0, 1:
			value = kind == 0
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 6, 7:
			if len(raw) < 2 {
				return nil, errors.New("truncated EventStream header length")
			}
			size = int(binary.BigEndian.Uint16(raw[:2]))
			raw = raw[2:]
		case 9:
			size = 16
		default:
			return nil, errors.New("unknown EventStream header type")
		}
		if len(raw) < size {
			return nil, errors.New("truncated EventStream header value")
		}
		switch kind {
		case 2:
			value = int8(raw[0])
		case 3:
			value = int16(binary.BigEndian.Uint16(raw[:2]))
		case 4:
			value = int32(binary.BigEndian.Uint32(raw[:4]))
		case 5:
			value = int64(binary.BigEndian.Uint64(raw[:8]))
		case 8:
			value = EventStreamTimestamp(binary.BigEndian.Uint64(raw[:8]))
		case 6:
			value = raw[:size]
		case 7:
			if !utf8.Valid(raw[:size]) {
				return nil, errors.New("invalid EventStream header string")
			}
			value = string(raw[:size])
		case 9:
			var uuid [16]byte
			copy(uuid[:], raw[:16])
			value = uuid
		}
		headers[name] = value
		raw = raw[size:]
	}
	return headers, nil
}
