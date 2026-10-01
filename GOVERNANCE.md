# Project governance

Crewship is a company-led open-source project developed and maintained by
**Unify Technology, s.r.o.**, a Czech limited liability company
(Company ID: 17266637). Registered company details are in [NOTICE](NOTICE).

## Maintenance and decisions

The company maintains the official repository and releases and is responsible
for project direction, review and acceptance of contributions, and release
decisions. Contribution does not confer a governance role or company ownership.

Use [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution and review process,
[RELEASING.md](RELEASING.md) for releases, and [SECURITY.md](SECURITY.md) for
private vulnerability reports. Privacy information is in [PRIVACY.md](PRIVACY.md).

## Licensing and attribution

The open-source core is distributed under [Apache License 2.0](LICENSE), which
permits commercial use subject to its terms. Company stewardship does not
replace or add restrictions to that license. A change of project ownership
does not by itself revoke licenses already granted for released versions.

Contributors retain copyright in their contributions under the existing
[contributor terms](CONTRIBUTING.md#license-and-contributor-terms). Dependencies
and other third-party materials retain their respective licenses and notices;
the company attribution does not claim ownership of those materials.
See [NOTICE](NOTICE) and [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).

The reserved enterprise directory has a separate licensing and contribution
policy documented in [ee/README.md](ee/README.md). That policy does not change
the license of the open-source core or establish a commercial offering.

## Commercial direction

During the current pre-release phase, the self-hosted open-source edition is
free to use, with no new commercial resource quotas being introduced. Users
remain responsible for their infrastructure and AI-provider costs. Feedback is
welcome and voluntary; providing a review or testimonial is not a condition
of the software license.

The company plans paid editions with higher resource allowances and additional
enterprise capabilities alongside a free community edition. Workspace, agent
and stored-credential counts are candidate metering dimensions, not published
plan limits. Pricing, numerical limits and the scope of each edition are not
established by this document.

Monetization is planned for a mature product, potentially at or after a stable
release; no start date is committed. Future commercial terms and upgrade
implications will be published before they take effect. This does not change
the Apache-2.0 permissions already granted for released code.

Product limits in an official build are distinct from copyright-license terms.
Apache-2.0 permits users to modify the covered code, including its resource-limit
checks. A signed entitlement does not remove those permissions. Separate
enterprise modules may have their own commercial terms, as described above.
Any proposal to require payment for modified Apache-covered installations must
be resolved as a separate licensing decision; it is not an existing restriction.

The current resource checks in [internal/license/enforce.go](internal/license/enforce.go)
are no-ops. The claims in [internal/license/license.go](internal/license/license.go)
are not evidence of enforced community quotas. Edition enforcement and final
commercial terms require separate implementation and review.

Apache-2.0 does not generally grant permission to use the licensor's trade names,
trademarks or product names beyond the uses described in section 6. This document
does not assert that a trademark has been registered.

Company identification and repository notices are not an assignment or license
agreement between an author and the company. Any agreements establishing the
company's rights are maintained separately from this public repository.
