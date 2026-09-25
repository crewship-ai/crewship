# Harbor Goods: one missed inquiry

The fixture is deliberately small: three inquiries, one unanswered for 26
sample hours. `check_leads.py` makes the decision from the data; it never reads
real mail or sends a message. The Page explains the story, the routine writes
its finding, and a human approval is needed before a draft is marked ready.

Files delivered into the Ops crew:

- `/crew/shared/demo/harbor-goods/leads.json`
- `/crew/shared/scripts/harbor-goods/check_leads.py`

Run **Check inquiries** from the Page to follow the whole example. The sample
records stay local to the crew; the approval only marks the draft ready. No
external service or secret is required for the deterministic check.
