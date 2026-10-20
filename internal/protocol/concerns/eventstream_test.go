package concerns

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"reflect"
	"testing"
	"time"
)

func eventFrame(headers, payload []byte) []byte {
	frame := make([]byte, 16+len(headers)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], payload)
	sealEventFrame(frame)
	return frame
}

func sealEventFrame(frame []byte) {
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
}

func eventHeader(name string, kind byte, value []byte) []byte {
	header := append([]byte{byte(len(name))}, []byte(name)...)
	header = append(header, kind)
	if kind == 6 || kind == 7 {
		header = binary.BigEndian.AppendUint16(header, uint16(len(value)))
	}
	return append(header, value...)
}

func TestEventStreamHeaderTypesAndTruncations(t *testing.T) {
	for _, tc := range []struct {
		kind byte
		raw  []byte
		want any
	}{
		{0, nil, true}, {1, nil, false},
		{2, []byte{255}, int8(-1)}, {3, []byte{255, 254}, int16(-2)},
		{4, []byte{255, 255, 255, 253}, int32(-3)},
		{5, bytes.Repeat([]byte{255}, 8), int64(-1)},
		{6, []byte{0, 255, 7}, []byte{0, 255, 7}}, {6, []byte{}, []byte{}},
		{7, []byte("λ"), "λ"}, {7, nil, ""},
		{8, []byte{0, 0, 0, 0, 0, 0, 0, 123}, int64(123)},
		{9, bytes.Repeat([]byte{42}, 16), [16]byte{42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42}},
	} {
		t.Run(fmt.Sprintf("type_%d/length_%d", tc.kind, len(tc.raw)), func(t *testing.T) {
			header := eventHeader("x", tc.kind, tc.raw)
			for cut := 1; cut <= len(header); cut++ {
				calls := 0
				err := ReadEventStream(context.Background(), bytes.NewReader(eventFrame(header[:cut], []byte("null"))), func(event EventStreamEvent) (bool, error) {
					calls++
					if !reflect.DeepEqual(event.Headers["x"], tc.want) || string(event.Payload) != "null" {
						t.Fatalf("header=%#v payload=%s", event.Headers, event.Payload)
					}
					return true, nil
				})
				if cut < len(header) {
					if err == nil || calls != 0 {
						t.Fatalf("type %d cut %d: calls=%d err=%v", tc.kind, cut, calls, err)
					}
				} else if err != nil || calls != 1 {
					t.Fatalf("type %d: calls=%d err=%v", tc.kind, calls, err)
				}
			}
		})
	}
}

func TestEventStreamRejectsMalformedFramesBeforeDelivery(t *testing.T) {
	valid := eventFrame(eventHeader(":event-type", 7, []byte("end")), []byte(`{}`))
	mutate := func(change func([]byte), reseal bool) []byte {
		frame := bytes.Clone(valid)
		change(frame)
		if reseal {
			sealEventFrame(frame)
		}
		return frame
	}
	bad := map[string][]byte{
		"absent": nil, "null": []byte("null"), "short": []byte{0},
		"prelude CRC":           mutate(func(b []byte) { b[8] ^= 1 }, false),
		"message CRC":           mutate(func(b []byte) { b[len(b)-1] ^= 1 }, false),
		"corrupt payload":       mutate(func(b []byte) { b[len(b)-5] ^= 1 }, false),
		"total under minimum":   mutate(func(b []byte) { binary.BigEndian.PutUint32(b[:4], 15) }, true),
		"total over limit":      mutate(func(b []byte) { binary.BigEndian.PutUint32(b[:4], MaxEventStreamMessageBytes+1) }, true),
		"total uint32 max":      mutate(func(b []byte) { binary.BigEndian.PutUint32(b[:4], ^uint32(0)) }, true),
		"headers over limit":    mutate(func(b []byte) { binary.BigEndian.PutUint32(b[4:8], MaxEventStreamHeadersBytes+1) }, true),
		"headers exceed frame":  mutate(func(b []byte) { binary.BigEndian.PutUint32(b[4:8], uint32(len(b)-15)) }, true),
		"declared body missing": mutate(func(b []byte) { binary.BigEndian.PutUint32(b[:4], uint32(len(b)+1)) }, true),
		"duplicate header":      eventFrame(append(eventHeader("x", 0, nil), eventHeader("x", 1, nil)...), nil),
		"empty name":            eventFrame([]byte{0, 0}, nil),
		"invalid name UTF8":     eventFrame([]byte{1, 255, 0}, nil),
		"invalid string UTF8":   eventFrame(eventHeader("x", 7, []byte{255}), nil),
		"unknown type":          eventFrame(eventHeader("x", 10, nil), nil),
	}
	for cut := 1; cut < len(valid); cut++ {
		bad[fmt.Sprintf("truncated/%d", cut)] = valid[:cut]
	}
	for kind := 10; kind <= 255; kind++ {
		bad[fmt.Sprintf("unknown type/%d", kind)] = eventFrame(eventHeader("x", byte(kind), nil), nil)
	}
	for name, frame := range bad {
		t.Run(name, func(t *testing.T) {
			for _, prefix := range []bool{false, true} {
				input, wantCalls := frame, 0
				if prefix {
					input, wantCalls = append(bytes.Clone(valid), frame...), 1
				}
				// Empty bytes after a terminal are the valid EOF case.
				if prefix && len(frame) == 0 {
					continue
				}
				for _, fragmented := range []bool{false, true} {
					var src io.Reader = bytes.NewReader(input)
					if fragmented {
						src = oneByteReader{src}
					}
					calls := 0
					err := ReadEventStream(context.Background(), src, func(EventStreamEvent) (bool, error) { calls++; return true, nil })
					if err == nil || calls != wantCalls {
						t.Fatalf("terminal prefix %v fragmented %v: calls=%d want=%d err=%v", prefix, fragmented, calls, wantCalls, err)
					}
				}
			}
		})
	}
}

type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(dst []byte) (int, error) { return r.Reader.Read(dst[:1]) }

func TestEventStreamPayloadShapesAndTerminals(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("null"), []byte(`""`), []byte("true"), []byte("42"), []byte("[]"), []byte("{}"), {0, 255}} {
		for _, terminal := range []bool{false, true} {
			for _, fragmented := range []bool{false, true} {
				frame := eventFrame(nil, payload)
				var src io.Reader = bytes.NewReader(frame)
				if fragmented {
					src = oneByteReader{src}
				}
				calls := 0
				err := ReadEventStream(context.Background(), src, func(event EventStreamEvent) (bool, error) {
					calls++
					if len(event.Headers) != 0 || !bytes.Equal(event.Payload, payload) {
						t.Fatalf("event=%+v", event)
					}
					return terminal, nil
				})
				if calls != 1 || (terminal && err != nil) || (!terminal && !errors.Is(err, io.ErrUnexpectedEOF)) {
					t.Fatalf("payload=%q terminal=%v fragmented=%v calls=%d err=%v", payload, terminal, fragmented, calls, err)
				}
			}
		}
	}
	frames := append(eventFrame(nil, []byte("data")), eventFrame(nil, []byte("end"))...)
	for cut := 1; cut < len(frames); cut++ {
		src := io.MultiReader(bytes.NewReader(frames[:cut]), bytes.NewReader(frames[cut:]))
		if err := ReadEventStream(context.Background(), src, func(e EventStreamEvent) (bool, error) { return string(e.Payload) == "end", nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventStreamBounds(t *testing.T) {
	for _, size := range []int{MaxEventStreamMessageBytes - 1, MaxEventStreamMessageBytes, MaxEventStreamMessageBytes + 1} {
		frame := eventFrame(nil, make([]byte, size-16))
		calls := 0
		err := ReadEventStream(context.Background(), bytes.NewReader(frame), func(EventStreamEvent) (bool, error) { calls++; return true, nil })
		if size <= MaxEventStreamMessageBytes {
			if err != nil || calls != 1 {
				t.Fatalf("size=%d calls=%d err=%v", size, calls, err)
			}
		} else if err == nil || calls != 0 {
			t.Fatalf("size=%d calls=%d err=%v", size, calls, err)
		}
	}
	for _, size := range []int{MaxEventStreamHeadersBytes - 1, MaxEventStreamHeadersBytes, MaxEventStreamHeadersBytes + 1} {
		headers := append(eventHeader("x", 6, make([]byte, 65500)), eventHeader("y", 6, make([]byte, 65500))...)
		headers = append(headers, eventHeader("z", 6, make([]byte, size-len(headers)-5))...)
		calls := 0
		err := ReadEventStream(context.Background(), bytes.NewReader(eventFrame(headers, nil)), func(EventStreamEvent) (bool, error) { calls++; return true, nil })
		if size <= MaxEventStreamHeadersBytes {
			if err != nil || calls != 1 {
				t.Fatalf("headers=%d calls=%d err=%v", size, calls, err)
			}
		} else if err == nil || calls != 0 {
			t.Fatalf("headers=%d calls=%d err=%v", size, calls, err)
		}
	}
	frame := eventFrame(nil, make([]byte, (1<<20)-16))
	for _, extra := range []bool{false, true} {
		readers := make([]io.Reader, 32)
		for i := range readers {
			readers[i] = bytes.NewReader(frame)
		}
		if extra {
			readers = append(readers, bytes.NewReader(eventFrame(nil, nil)))
		}
		calls := 0
		err := ReadEventStream(context.Background(), io.MultiReader(readers...), func(EventStreamEvent) (bool, error) { calls++; return true, nil })
		if calls != 32 || (!extra && err != nil) || (extra && err == nil) {
			t.Fatalf("extra=%v calls=%d err=%v", extra, calls, err)
		}
	}
}

type eventReadError struct{ err error }

func (r eventReadError) Read([]byte) (int, error) { return 0, r.err }

func TestEventStreamPreludeGuardsBeforeBodyRead(t *testing.T) {
	bodyRead := errors.New("body read reached")
	for _, length := range []uint32{0, 1, 11, 12, 15, 16, MaxEventStreamMessageBytes, MaxEventStreamMessageBytes + 1, ^uint32(0)} {
		for _, headers := range []uint32{0, 1, MaxEventStreamHeadersBytes - 1, MaxEventStreamHeadersBytes, MaxEventStreamHeadersBytes + 1, ^uint32(0)} {
			for _, validCRC := range []bool{false, true} {
				prelude := make([]byte, 12)
				binary.BigEndian.PutUint32(prelude[:4], length)
				binary.BigEndian.PutUint32(prelude[4:8], headers)
				binary.BigEndian.PutUint32(prelude[8:], crc32.ChecksumIEEE(prelude[:8]))
				if !validCRC {
					prelude[8] ^= 1
				}
				err := ReadEventStream(context.Background(), io.MultiReader(bytes.NewReader(prelude), eventReadError{bodyRead}), func(EventStreamEvent) (bool, error) {
					t.Fatal("incomplete body delivered")
					return true, nil
				})
				want := "EventStream frame bounds are invalid"
				if !validCRC {
					want = "EventStream prelude CRC mismatch"
				} else if length >= 16 && length <= MaxEventStreamMessageBytes && headers <= MaxEventStreamHeadersBytes && uint64(headers)+16 <= uint64(length) {
					want = bodyRead.Error()
				}
				if err == nil || err.Error() != want {
					t.Fatalf("length=%d headers=%d CRC=%v: err=%v want=%s", length, headers, validCRC, err, want)
				}
			}
		}
	}
}

func TestEventStreamErrorsAndCancellation(t *testing.T) {
	frame := eventFrame(nil, nil)
	cause := errors.New("fixture failure")
	for _, cut := range []int{0, 5, 12, len(frame)} {
		src := io.MultiReader(bytes.NewReader(frame[:cut]), eventReadError{cause})
		if err := ReadEventStream(context.Background(), src, func(EventStreamEvent) (bool, error) { return true, nil }); !errors.Is(err, cause) {
			t.Fatalf("cut=%d err=%v", cut, err)
		}
	}
	if err := ReadEventStream(context.Background(), bytes.NewReader(frame), func(EventStreamEvent) (bool, error) { return true, cause }); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ReadEventStream(ctx, bytes.NewReader(frame), func(EventStreamEvent) (bool, error) { t.Fatal("delivered after cancellation"); return true, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := ReadEventStream(ctx, bytes.NewReader(frame), func(EventStreamEvent) (bool, error) { cancel(); return true, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := ReadEventStream(context.Background(), nil, func(EventStreamEvent) (bool, error) { return true, nil }); err == nil {
		t.Fatal("nil reader accepted")
	}
	if err := ReadEventStream(context.Background(), bytes.NewReader(frame), nil); err == nil {
		t.Fatal("nil consumer accepted")
	}
}

func TestEventStreamCancelDuringPartialRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- ReadEventStream(ctx, reader, func(EventStreamEvent) (bool, error) {
			return false, errors.New("partial frame reached consumer")
		})
	}()
	if _, err := writer.Write(eventFrame(nil, nil)[:5]); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = reader.CloseWithError(ctx.Err())
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial read did not unwind")
	}
}

func FuzzEventStreamHeaders(f *testing.F) {
	for kind := byte(0); kind <= 10; kind++ {
		f.Add(eventHeader("x", kind, make([]byte, 16)))
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, headers []byte) {
		if len(headers) > MaxEventStreamHeadersBytes {
			return
		}
		_ = ReadEventStream(context.Background(), bytes.NewReader(eventFrame(headers, []byte("null"))), func(EventStreamEvent) (bool, error) { return true, nil })
	})
}

func FuzzEventStreamFrames(f *testing.F) {
	f.Add(eventFrame(eventHeader(":event-type", 7, []byte("end")), []byte("null")))
	f.Add([]byte("null"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = ReadEventStream(context.Background(), bytes.NewReader(raw), func(EventStreamEvent) (bool, error) { return true, nil })
	})
}

func BenchmarkReadEventStream(b *testing.B) {
	frame := eventFrame(eventHeader(":event-type", 7, []byte("end")), bytes.Repeat([]byte("x"), 4096))
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := ReadEventStream(context.Background(), bytes.NewReader(frame), func(EventStreamEvent) (bool, error) { return true, nil }); err != nil {
			b.Fatal(err)
		}
	}
}
