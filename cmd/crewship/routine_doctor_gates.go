package main

import "fmt"

func checkValidationGates(def map[string]interface{}) []doctorCheck {
	steps, ok := def["steps"].([]interface{})
	if !ok {
		return []doctorCheck{{Name: "validation_gates", Level: doctorOK, Message: "no validation blocks to check"}}
	}
	out := make([]doctorCheck, 0, len(steps))
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		stepID, _ := step["id"].(string)
		v, ok := step["validation"].(map[string]interface{})
		if !ok {
			continue
		}
		minLen := optionalInt(v, "min_length")
		maxLen := optionalInt(v, "max_length")
		if minLen != nil && maxLen != nil && *minLen > *maxLen {
			out = append(out, doctorCheck{
				Name:    "validation:" + stepID,
				Level:   doctorFail,
				Message: fmt.Sprintf("step %q validation has min_length %d > max_length %d", stepID, *minLen, *maxLen),
				Hint:    "no output can satisfy this gate — fix the bounds",
			})
			continue
		}
		mc, _ := v["must_contain"].([]interface{})
		mnc, _ := v["must_not_contain"].([]interface{})
		// Detect direct contradiction: same string in both lists.
		for _, c := range mc {
			cs, _ := c.(string)
			for _, n := range mnc {
				ns, _ := n.(string)
				if cs != "" && cs == ns {
					out = append(out, doctorCheck{
						Name:    "validation:" + stepID,
						Level:   doctorFail,
						Message: fmt.Sprintf("step %q validation: %q is in BOTH must_contain and must_not_contain", stepID, cs),
						Hint:    "no output can satisfy a contradictory gate — remove from one list",
					})
					goto nextStep
				}
			}
		}
		out = append(out, doctorCheck{
			Name:    "validation:" + stepID,
			Level:   doctorOK,
			Message: "gate is structurally satisfiable",
		})
	nextStep:
	}
	if len(out) == 0 {
		return []doctorCheck{{Name: "validation_gates", Level: doctorOK, Message: "no validation blocks to check"}}
	}
	return out
}
