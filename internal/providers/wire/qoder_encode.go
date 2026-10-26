package wire

import "encoding/base64"

const (
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

// qoderSubstitution maps one standard base64 character to its custom one
// (encoding.js:15-25); "=" becomes "$".
var qoderSubstitution = func() (table [256]byte) {
	for i := range table {
		table[i] = byte(i)
	}
	for i := 0; i < 64; i++ {
		table[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
	}
	table['='] = '$'
	return table
}()

// QoderEncodeBody is qoderEncodeBody (encoding.js:32-55): base64, then the string
// is rearranged as [last third][middle][first third] with a = floor(n/3), then each
// character is substituted. The result is ASCII; it is sent with `&Encode=1` and the
// COSY signature covers these bytes.
func QoderEncodeBody(plain []byte) []byte {
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	a := n / 3
	out := make([]byte, 0, n)
	out = append(out, std[n-a:]...)
	out = append(out, std[a:n-a]...)
	out = append(out, std[:a]...)
	for i, c := range out {
		out[i] = qoderSubstitution[c]
	}
	return out
}
