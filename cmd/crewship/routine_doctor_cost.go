package main

import "fmt"

func checkCostCap(def map[string]interface{}) doctorCheck {
	cap, _ := def["max_cost_usd"].(float64)
	est, _ := def["estimated_cost_usd"].(float64)

	if cap == 0 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorWarn,
			Message: "max_cost_usd not set",
			Hint:    "without a cap, a runaway tier escalation can spend uncapped — set max_cost_usd to ~10× estimated_cost_usd",
		}
	}
	if est == 0 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorOK,
			Message: fmt.Sprintf("max_cost_usd=$%.4f set; no estimate to compare", cap),
		}
	}
	if cap < est*1.5 {
		return doctorCheck{
			Name:    "cost_cap",
			Level:   doctorWarn,
			Message: fmt.Sprintf("max_cost_usd $%.4f is < 1.5× estimated $%.4f", cap, est),
			Hint:    "tier escalation or grader iterations will likely trip the cap; widen to 10× estimate",
		}
	}
	return doctorCheck{
		Name:    "cost_cap",
		Level:   doctorOK,
		Message: fmt.Sprintf("max $%.4f cap is %.1f× the estimated $%.4f", cap, cap/est, est),
	}
}
