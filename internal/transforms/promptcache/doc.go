// Package promptcache owns final-body cache anchoring (SPEC §18). Anchoring is
// applied after every token saver so markers describe the exact outbound
// prefix, never a prefix a later transform rewrites.
package promptcache
