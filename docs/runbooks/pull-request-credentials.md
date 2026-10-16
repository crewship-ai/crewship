# Diagnose pull-request credentials

Use this procedure when attaching or refreshing an issue's pull-request link
fails. Chat cards read stored link metadata; polling that list does not fetch
the forge or provide live CI, diffs or test evidence.

1. Read the response's machine-readable `code`, not just its HTTP status.
   `412` with `no-credential` is generated locally before a forge request:
   the application found no eligible stored credential. It does not mean
   GitHub rejected a token. `credential-unreadable` instead reports a local
   decryption failure; check the installation's encryption-key configuration
   without replacing it or printing secrets.
2. In the application's authorized credential management, check the same
   workspace as the issue. The credential must have provider `GITHUB` or
   `GITLAB`, be ACTIVE and not deleted, and be workspace-wide or scoped to
   the issue's crew. A developer's `gh` login or a host's GitHub App is not
   automatically a credential in this application vault.
3. For a self-hosted forge, set the credential's account label to its exact
   hostname (case-insensitive). The canonical `github.com`/`gitlab.com`
   hosts may fall back to the oldest eligible provider credential; other
   hosts do not. Never solve a host mismatch by broadening credential scope.
4. Have the credential owner provide the intended repository access through
   the installation's approved provisioning process. On managed Crewship
   hosts, infrastructure changes require an Ansible branch, reviewed MR and
   successful CI before apply. Do not copy development-host credentials,
   print token values, or modify installed configuration by hand. If no
   approved credential is available, record provisioning as blocked and
   retain the stored-data label; do not claim live synchronization works.
5. Verify with the existing CLI after provisioning:

   ```sh
   crewship issue link ENG-4 https://github.com/acme/example/pull/7
   crewship issue links ENG-4
   crewship issue relink ENG-4 LINK_ID
   ```

   Use an issue and repository you are authorized to modify. A successful
   refresh updates stored provider metadata, not live CI. Distinguish
   provider permission/revocation/rate-limit/outage codes from the local
   `no-credential` precondition. Keep diagnostic evidence to status, code
   and sanitized scope/counts; exclude secrets and conversation contents.

Implementation: `resolveCodeLinkCredential` and
`replyCodeLinkCredentialError` in `internal/api/issue_code_links.go`;
existing attach and refresh tests cover the `no-credential` response.
