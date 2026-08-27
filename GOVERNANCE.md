# Governance

## Who runs this

`k8s-status` is maintained by [@ntmggr](https://github.com/ntmggr), who is currently the
only person with write access to the repository, the container registry, and the
Actions secrets used to publish releases.

That is worth stating plainly rather than implying a larger group. A single maintainer
means changes land quickly and that there is no second reviewer, which is why the
automated checks in CI are the real gate rather than human review.

## Roles

**Maintainer.** Merges pull requests, cuts releases, holds the publishing credentials,
and responds to security reports. Currently one person.

**Contributor.** Anyone opening an issue or a pull request. No permissions on the
repository are needed, and none are granted for a contribution.

## How decisions are made

Changes are proposed as pull requests and discussed there or in an issue. There is no
formal voting; the maintainer decides, and the reasoning goes in the pull request so it
is on the record.

Anything that widens what the service can read from a cluster, adds a dependency, or
changes the security posture is expected to say so explicitly in the pull request
description, because those are the changes that are hard to reverse once released.

## Granting access

Write access is granted only after a sustained history of contributions, and it is
granted deliberately rather than by default. GitHub gives new collaborators read access
unless something more is chosen.

## If this project becomes unmaintained

If no maintainer has responded for 90 days, treat the project as unmaintained. Open an
issue asking about its status before depending on it further. The licence permits
forking, and that is the intended remedy.
