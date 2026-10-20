package ingress

import (
	"encoding/binary"
	"hash/crc32"
)

func kiroFrame(kind, payload string) []byte {
	header := []byte{11}
	header = append(header, ":event-type"...)
	header = append(header, 7, 0, byte(len(kind)))
	header = append(header, kind...)
	frame := make([]byte, 16+len(header)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(header)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], header)
	copy(frame[12+len(header):], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

func kiroToolTurn() []byte {
	var body []byte
	for _, frame := range [][]byte{
		kiroFrame("toolUseEvent", `{"toolUseId":"t","name":"a_b","input":{}}`),
		kiroFrame("messageStopEvent", `{"stopReason":"tool_use"}`),
	} {
		body = append(body, frame...)
	}
	return body
}
