// Package parity holds drift guards that compare Routeweft with the pinned
// LiteRouter oracle. It has no production code: the tests read the oracle's own
// source through git (never its working files) and fail when LiteRouter has a
// provider, format or conversion leg that Routeweft lacks. See
// compat/parity/README.md.
package parity
