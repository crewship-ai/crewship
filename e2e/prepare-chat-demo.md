# Persistent Dev2 Chat demo

Run `python3 e2e/prepare-chat-demo.py` with the existing authenticated `dev2`
owner CLI profile. The script accepts `--binary`, `--server` and `--profile`;
it refuses any server except local port 8082.

It adds two explicitly labelled fictional colleagues, Klára and Tomáš, through
normal member provisioning, account setup, separate CLI login and self-service
profile updates. Their `.invalid` email addresses receive no email. The owner’s
profile and avatar are unchanged. It creates/reuses `Týmové plánování · demo`,
a three-author fictional planning exchange and one synthetic private greeting
from each demo colleague to the owner. It never adds an agent, executes a model,
creates an issue or enables automated activity. Channel visibility follows the
normal workspace ACL.

Passwords and isolated CLI configs exist only in
`/srv/crewship/.dev2-chat-demo/` (0700; files 0600), outside this repository.
The script prints IDs and the private state path, never passwords or setup links.
Do not copy that directory into a report or commit. The script refuses to
reprovision an existing account for which it has no locally owned credentials.
It reuses room IDs and stable client message IDs; a second run verifies that
exactly three initial channel messages and one greeting per DM remain, with the
expected distinct authors. No demo data is deleted at the end because the user
requested colleagues available for continued UI exploration.

The initial demo uses the product’s deterministic initials avatars. Users can
upload their own profile image through the existing self-service avatar flow.
