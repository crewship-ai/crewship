package pipeline

import "fmt"

// validateStepOutputGate rejects a step whose `validation` block is
// unsatisfiable — no output could ever pass it. Two shapes:
//
//   - min_length > max_length: an impossible length window.
//   - the same token in both must_contain and must_not_contain: a direct
//     contradiction.
//
// These used to be caught only by `doctor` (post-save, live). Folding the
// DB-free subset into Validate means the editor loop, `routine validate`, and
// `save` all reject a self-contradicting gate before it ships and fails every
// run. (min_length == max_length is satisfiable — an exact-length output — so
// only strict `>` is rejected.)
func validateStepOutputGate(st Step) error {
	v := st.Validation
	if v == nil {
		return nil
	}
	if v.MinLength != nil && v.MaxLength != nil && *v.MinLength > *v.MaxLength {
		return fmt.Errorf("pipeline: step %q validation min_length %d exceeds max_length %d — no output can satisfy this gate", st.ID, *v.MinLength, *v.MaxLength)
	}
	if len(v.MustContain) > 0 && len(v.MustNotContain) > 0 {
		forbidden := make(map[string]struct{}, len(v.MustNotContain))
		for _, n := range v.MustNotContain {
			forbidden[n] = struct{}{}
		}
		for _, c := range v.MustContain {
			if _, clash := forbidden[c]; clash {
				return fmt.Errorf("pipeline: step %q validation: %q is in both must_contain and must_not_contain — no output can satisfy this gate", st.ID, c)
			}
		}
	}
	return nil
}
