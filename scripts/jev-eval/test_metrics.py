import unittest
from run import metrics


class MetricsTest(unittest.TestCase):
    def test_empty_does_not_report_perfect_accuracy(self):
        self.assertIsNone(metrics([])["accuracy"])

    def test_abstention_and_calibration_use_different_denominators(self):
        def row(area, expected, p, review, confidence):
            return dict(area=area, expected=expected, probabilities={area: p, "other": 1-p},
                        needs_review=review, confidence=confidence, latency_ms=10,
                        signals=dict(production_impact=0, requests_destructive_action=0),
                        expected_signals=dict(production_impact=False, requests_destructive_action=False))
        got = metrics([row("api", "api", .8, False, .1), row("docs", "other", .6, True, .99)])
        self.assertEqual(got["accuracy"], .5)
        self.assertEqual(got["coverage"], .5)
        self.assertEqual(got["selective_accuracy"], 1)
        self.assertAlmostEqual(got["top_probability_ece_10_bins"], .4)
        self.assertAlmostEqual(got["multiclass_brier_sum"], .4)

    def test_no_accepted_rows_have_unknown_selective_accuracy(self):
        row = dict(area="unknown", expected="unknown", probabilities={"unknown": 1.0},
                   needs_review=True, latency_ms=2, signals=dict(production_impact=0, requests_destructive_action=0),
                   expected_signals=dict(production_impact=False, requests_destructive_action=False))
        self.assertIsNone(metrics([row])["selective_accuracy"])


if __name__ == "__main__":
    unittest.main()
