package memport

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/memory"
)

// Placer moves validated documents from a staging directory to where they
// finally live.
//
// # Why placement is separable at all
//
// The memory tree an agent uses is owned by the CONTAINER user: the
// docker provider sets `.memory` to 1001:1002 mode 2775
// (buildChownInitCmd), so write belongs to group 1002 and the host
// process — which is in neither — can read it and cannot write it.
// Every other writer into agent memory already runs on the container
// side; an operator-initiated import is the one that does not.
//
// So policy and placement happen in different places. Policy stays
// exactly where it was: Apply runs the caps, the scrubber, the
// confinement checks and memory.WriteFile against a staging directory
// this process owns. Only the final move crosses the boundary, through
// whatever the deployment makes possible.
//
// Splitting it this way rather than teaching Apply about containers is
// deliberate — it keeps one write chokepoint with one set of rules, and
// leaves the transport a detail of the caller.
type Placer interface {
	// Place moves the documents named by rels (paths relative to
	// stagingDir, forward-slashed) into the destination the
	// implementation owns. It must be all-or-nothing from the caller's
	// point of view: on error, the caller reports nothing as written.
	Place(ctx context.Context, stagingDir string, rels []string) error
}

// OperatorError is a placement failure whose cause is something the
// operator can act on — a stopped crew, say — rather than an internal
// transport detail. Its message is shown to them verbatim, so it must
// name no container id, no host path and no internal identifier.
type OperatorError interface {
	error
	OperatorMessage() string
}

// ApplyVia validates documents exactly as Apply does, into a private
// staging directory, and then hands the result to placer.
//
// A placement failure is not a partial success: the documents Apply
// wrote went into a temporary directory that is about to be deleted, so
// they are reported as failures rather than as writes. Reporting them
// written because Apply succeeded would be the same lie as a green
// check on a review that never ran.
func ApplyVia(ctx context.Context, placer Placer, docs []Doc, cfg memory.WriteConfig) (ApplyResult, error) {
	if placer == nil {
		return ApplyResult{}, fmt.Errorf("memport: no placer configured for this import")
	}
	staging, err := os.MkdirTemp("", "crewship-memport-")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("memport: staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	res, err := Apply(ctx, staging, docs, cfg)
	if err != nil {
		return res, err
	}
	if len(res.Written) == 0 {
		return res, nil
	}

	if perr := placer.Place(ctx, staging, res.Written); perr != nil {
		// A placer that knows what the OPERATOR should do says so, and
		// that message reaches them. Anything else keeps the generic
		// text, because a transport error carries container ids and
		// host paths that a client has no business receiving.
		// "Placement failed" does NOT mean "nothing landed". The
		// transport is a tar extraction, which is not atomic: it writes
		// entries until one fails. Observed on dev3 — PERSONA.md and
		// daily/ were on disk while this path reported nothing written.
		// The operator is told the truth, which is that the state is
		// now unknown and re-running is safe.
		reason := "placement failed partway — some documents may already be in the crew's memory; fix the cause and import again. See the server log."
		var op OperatorError
		if errors.As(perr, &op) {
			reason = op.OperatorMessage()
		}
		for _, rel := range res.Written {
			res.Failed = append(res.Failed, Failure{
				RelPath: rel,
				Reason:  reason,
				Cause:   perr,
			})
		}
		res.Written = nil
		return res, nil
	}
	return res, nil
}
