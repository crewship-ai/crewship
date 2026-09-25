#!/usr/bin/env python3
"""Read the Harbor Goods fixture and report unanswered leads. No network or AI."""

import argparse
import json
from pathlib import Path


def check(path: Path, threshold: int) -> dict:
    try:
        source = json.loads(path.read_text(encoding="utf-8"))
        leads = source["leads"]
        if not isinstance(leads, list) or len(leads) != 3:
            raise ValueError("expected three sample leads")
        overdue = [lead for lead in leads if not lead["replied"] and lead["hours_waiting"] >= threshold]
        oldest = max(overdue, key=lambda lead: lead["hours_waiting"]) if overdue else None
        return {
            "state": "warning" if oldest else "ok",
            "headline": f"{len(overdue)} of {len(leads)} inquiries need attention" if oldest else "All inquiries have a reply",
            "lead_id": oldest["id"] if oldest else "none",
            "customer": oldest["customer"] if oldest else "none",
            "request": oldest["request"] if oldest else "none",
            "age": f"{oldest['hours_waiting']} hours" if oldest else "none",
            "next_step": "Prepare a reply for human approval" if oldest else "No follow-up needed",
            "source": "Sample data in /crew/shared/demo/harbor-goods/leads.json",
        }
    except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
        return {
            "state": "critical", "headline": "Demo data could not be checked",
            "lead_id": "unknown", "customer": "unknown", "request": "unknown",
            "age": "unknown", "next_step": f"Check the sample file: {type(exc).__name__}",
            "source": "Sample data in /crew/shared/demo/harbor-goods/leads.json",
        }


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", type=Path, default=Path("/crew/shared/demo/harbor-goods/leads.json"))
    parser.add_argument("--after-hours", type=int, default=24)
    args = parser.parse_args()
    print(json.dumps(check(args.file, args.after_hours), ensure_ascii=False))
