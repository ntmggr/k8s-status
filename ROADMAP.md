# Roadmap

This page is direction, not a schedule, and nothing on it is a promise from anyone.
Work starts as an issue or a pull request; this page says which direction those should
point.

## GitOps coverage

Flux support is younger than ArgoCD support, and what it does not do is listed in
[What Flux support does not cover](README.md#what-flux-support-does-not-cover): no
`DRIFT` verdict, no `PRUNE`, no environment header. Part of that is inherent to what
Flux reports, and a page that invents the missing verdicts would be a guess presented
as fact; that part should stay absent. The rest is work that has not been done yet.
Telling the two apart is the first step, and where the answer is "Flux reports this and
the page ignores it", the page should stop ignoring it.

## Operational confidence

The browser tests screenshot every pull request and compare it against `main`'s
rendering, but the comparison is advisory and does not block a merge. It should become
a required check once it has been stable long enough, so that a deliberate visual
change has to update the baseline in the same merge that makes it.

## Project maturity

The project intends to apply to the CNCF Sandbox once it can meet the bar. The bar comes
from the [Sandbox process](https://github.com/cncf/sandbox) and the
[project lifecycle](https://contribute.cncf.io/projects/lifecycle/); this is where the
project stands against it:

- **Licence.** Apache-2.0. Done.
- **Maintainers.** [MAINTAINERS.md](MAINTAINERS.md) lists two, both independent. The
  CNCF project template asks Sandbox projects for at least three maintainers from two
  or more organisations, so the gap is people, not paperwork.
- **Age.** An application needs a repository at least six months old with active
  development behind it. This one is younger, so the wait is real, and there is time in
  it to close the rest of this list.
- **The name.** It begins with `k8s`, and projects taking a prefix like that are asked
  to clear it with the Kubernetes leadership group rather than assume it is fine. Better
  to have that conversation early, since the answer could be a rename.
- **Adopters.** Recommended rather than required. [ADOPTERS.md](ADOPTERS.md) is empty,
  and an empty list is the honest thing to show.

Beyond the Sandbox itself, the OpenSSF [Best Practices](https://bestpractices.coreinfrastructure.org/) badge is the next step for the security story: the OpenSSF Scorecard already runs against the repository, so the posture is measured rather than asserted.
