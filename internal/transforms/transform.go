// Package transforms owns the Routeweft request-transform pipeline. It runs
// token savers over a neutral request view in the normative order
// (BDR-012, SPEC §17): RTK -> Headroom -> Caveman -> Ponytail -> PXPIPE.
package transforms

import (
	"context"
	"slices"
)

// Request is a protocol-neutral view of messages plus the system/instruction
// head. Implementations own mapping to/from their concrete wire body.
type Request struct {
	System   []string
	Messages []Message
	// Bypass is the client per-request opt-out for token savers.
	Bypass bool
	// Model is the model the request is addressed to, for helpers that report it.
	Model string
	// Protocol names the wire protocol the view was read from.
	Protocol string
	// Unsafe, when set, is why a helper must not be given this conversation (a
	// Responses input that holds tool or reasoning items, a protocol the helper does
	// not speak). Helpers that cannot honor it leave the request unchanged.
	Unsafe string
}

// Message is one conversation turn with an opaque role and content.
type Message struct {
	Role    string
	Content string
	// ToolResult marks a tool/function result turn eligible for RTK compression.
	ToolResult bool
}

// Clone returns a deep copy so a transform can be attempted without mutating
// the caller's request when it fails.
func (r Request) Clone() Request {
	cloned := r
	cloned.System = slices.Clone(r.System)
	cloned.Messages = slices.Clone(r.Messages)
	return cloned
}

// Transform mutates a cloned request. Returning an error marks the step failed;
// the pipeline keeps the pre-step request because transforms are fail-open
// (PRD-XFORM-003).
type Transform interface {
	Name() string
	Apply(Request) (Request, error)
}

// Pipeline applies transforms in the exact order given. A nil transform is
// skipped, so callers can register optional steps conditionally.
type Pipeline struct {
	Steps []Transform
}

// Run applies each step fail-open: a step error discards that step's output and
// keeps the request unchanged, then the pipeline continues.
func (p Pipeline) Run(request Request) Request {
	return p.RunContext(context.Background(), request)
}

func (p Pipeline) RunContext(ctx context.Context, request Request) Request {
	current := request.Clone()
	if current.Bypass {
		return current
	}
	for _, step := range p.Steps {
		if ctx.Err() != nil {
			break
		}
		if step == nil {
			continue
		}
		var updated Request
		var err error
		if contextual, ok := step.(interface {
			ApplyContext(context.Context, Request) (Request, error)
		}); ok {
			updated, err = contextual.ApplyContext(ctx, current.Clone())
		} else {
			updated, err = step.Apply(current.Clone())
		}
		if err != nil {
			continue
		}
		current = updated
	}
	return current
}
