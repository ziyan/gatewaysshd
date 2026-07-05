<!--
  PR description tips:
  - Lead with one line explaining what changes and why. The diff shows what.
  - Link related PRs / issues below.
  - Keep it short. Reviewers will ask if they need more context.
-->

## Summary

<!-- One or two sentences on what changed and why. -->

<!-- changelog:start -->
## Changelog

<!--
Required. Replace the heading below with one of:
  ### Added       — new behavior
  ### Changed     — change to existing behavior
  ### Deprecated  — marked for removal
  ### Removed     — removed feature
  ### Fixed       — bug fix
  ### Security    — security fix

Replace the bullet with a one-line description of the user-visible change.
Duplicate the heading + bullet block if your PR spans multiple sections.

The release bot reads each merged PR's block and writes the next
`## version` section in CHANGELOG.md, attributed with `(#number)`.

If this PR has no user-visible change (CI tweak, docs typo, internal-only
refactor), apply the `skip-changelog` label.

Keep the changelog:start / changelog:end markers — CI locates the block
by them. If you write the description outside the GitHub web UI (e.g.
`gh pr create --body`), this template is NOT applied automatically: copy
this whole block, markers included, into your description.

The Changelog check reads the description from the event that triggered
CI; editing the description alone does not re-run it. After fixing the
block, push a commit or close and reopen the PR.
-->

### Added | Changed | Deprecated | Removed | Fixed | Security

- TODO: replace with a one-line summary of the user-visible change.
<!-- changelog:end -->

## Test plan

<!-- One line per check, e.g. "make test", "manual tunnel through a local gateway". -->
