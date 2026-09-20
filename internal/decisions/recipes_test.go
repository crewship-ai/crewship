package decisions

import (
	"encoding/json"
	"testing"
)

func triageResponse(req Request) Response {
	probs := map[string]*float64{}
	var criteria map[string]string
	_ = json.Unmarshal(req.Questions["area"].Criteria, &criteria)
	for k := range criteria {
		probs[k] = ptr(0.0)
	}
	probs["api"] = ptr(1.0)
	return Response{Model: "test", Usage: Usage{InputTokens: ptr(int64(1)), OutputTokens: ptr(int64(1))}, Answers: map[string]Answer{
		"area":                        {Type: "choice", Choice: "api", Confidence: ptr(0.95), Probabilities: probs},
		"has_reproduction":            {Type: "noul", Noul: ptr(0.8)},
		"production_impact":           {Type: "noul", Noul: ptr(0.0)},
		"requests_destructive_action": {Type: "noul", Noul: ptr(0.0)},
	}}
}
func TestTriageReviewGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Response)
		reason string
	}{
		{"clear", func(*Response) {}, "advisory_suggestion"},
		{"uncertain", func(r *Response) { a := r.Answers["area"]; a.Confidence = ptr(0.6); r.Answers["area"] = a }, "low_confidence"},
		{"destructive", func(r *Response) {
			a := r.Answers["requests_destructive_action"]
			a.Noul = ptr(0.9)
			r.Answers["requests_destructive_action"] = a
		}, "destructive_action_signal"},
		{"outage", func(r *Response) {
			a := r.Answers["production_impact"]
			a.Noul = ptr(0.5)
			r.Answers["production_impact"] = a
		}, "production_impact_signal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := TriageRequest("sample")
			resp := triageResponse(req)
			tc.change(&resp)
			got, err := SuggestTriage(req, resp, 0.9)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reason != tc.reason || got.NeedsReview != (tc.reason != "advisory_suggestion") {
				t.Fatalf("wrong gate %+v", got)
			}
		})
	}
}
func TestRerankKeepsAllIDsAndStableTies(t *testing.T) {
	in := RerankInput{Query: "rollback", Candidates: []Candidate{{"first", "irrelevant"}, {"second", "rollback steps"}, {"third", "more steps"}}}
	req, err := RerankRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	resp := Response{Model: "test", Usage: Usage{InputTokens: ptr(int64(1)), OutputTokens: ptr(int64(1))}, Answers: map[string]Answer{}}
	for i, score := range []int{0, 3, 3} {
		key := string(rune('0' + i))
		probs := map[string]*float64{"0": ptr(0.0), "1": ptr(0.0), "2": ptr(0.0), "3": ptr(0.0)}
		probs[string(rune('0'+score))] = ptr(1.0)
		resp.Answers[key] = Answer{Type: "score", Score: ptr(float64(score)), Confidence: ptr(1.0), Probabilities: probs}
	}
	if err = resp.Validate(req); err != nil {
		t.Fatal(err)
	}
	ranked, err := Rank(in, resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 3 || ranked[0].ID != "second" || ranked[1].ID != "third" || ranked[2].ID != "first" || in.Candidates[0].ID != "first" {
		t.Fatalf("lost identity/order %+v", ranked)
	}
	delete(resp.Answers, "2")
	if _, err = Rank(in, resp); err == nil {
		t.Fatal("ranked incomplete result")
	}
	in.Candidates[2].ID = "first"
	if _, err = RerankRequest(in); err == nil {
		t.Fatal("accepted duplicate id")
	}
}
