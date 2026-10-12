# Manifest demo: a crew and one checkable issue

These first two steps create a separate Lab crew with Mia and Leo, then a
project, label and issue whose result is a file with one known line. They
complement the narrated feature scenarios in [scripts/demo](../../scripts/demo/README.md).

Use a disposable instance or dedicated test workspace. Select and authenticate
your CLI profile before applying these files. The examples neither bootstrap
an account nor read credentials from the checkout.

## Create the crew

Inspect the model choices in `00-crew/lab.crew.yaml`, then preview each file:

```bash
crewship apply --file examples/demo-v2/00-crew/lab.crew.yaml --dry-run --no-delete
crewship apply --file examples/demo-v2/00-crew/10-foundations.yaml --dry-run --no-delete
```

Apply each file by removing `--dry-run` after reviewing its plan. `--no-delete`
refuses plans containing deletions; it does not guarantee that an existing
resource will be left unchanged. Use an empty test workspace to avoid name
collisions with existing `lab`, `mia`, `leo` or `demo-v2` resources.

Before running an agent, bind your existing model credential to both agents
using [credential assignment](../../docs/cli/credential.mdx), with the
environment slot matching the credential type. No credential value belongs
in these manifests. Provisioning and an agent run require the usual Docker
and model-provider prerequisites.

## Create and run the issue

```bash
crewship apply --file examples/demo-v2/01-issues/01-hello-file.yaml --dry-run --no-delete
```

Review the plan and apply by removing `--dry-run`. In Issues, open the new
hello-file issue and use its displayed identifier to start it:

```bash
crewship issue start <issue-id>
crewship crew files get lab shared/demo/hello.txt
```

Check that the file contains exactly `hello from Crewship` and the issue has
Leo's comment with the path and content. Inspect its activity and session in
the web UI. Model execution can use provider credits; applying the manifests
alone is not evidence that the task ran.

The historical automatic `init` and destructive `reset` runner is intentionally
not part of these examples. Reapplying is not a guaranteed rerun of a completed
issue; use another owned test workspace for a fresh acceptance pass. Further
per-feature manifest steps remain work tracked in #2427.
