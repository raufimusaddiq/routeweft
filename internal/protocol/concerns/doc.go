// Package concerns holds the conversion rules that every wire-format leg shares:
// finish-reason mapping, tool-call identity, tool-name sanitising, reasoning
// extraction and usage shape. Each rule cites the pinned oracle's
// translator/concerns file it mirrors; a leg never re-implements one locally.
package concerns
