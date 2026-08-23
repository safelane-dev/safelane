// Package oci resolves the exact immutable container SafeLane is about to
// deploy, and proves which source revision it was built from.
//
// It talks to any OCI Distribution registry - GHCR, Docker Hub, ECR, a
// registry running in a test process - reachable anonymously or through the
// Docker credentials the user already has. There is no GHCR-shaped special
// case, because "which registry is this" is not a question that should change
// what SafeLane is willing to believe.
//
// # Provenance is proved, never guessed
//
// The binding from a container to a commit comes from one of four places, and
// each one is recorded by name in the evidence:
//
//   - [BindingOCILabels] - the image's own `org.opencontainers.image.source`
//     and `org.opencontainers.image.revision`, read from every runnable
//     platform in the index and required to agree;
//   - [BindingCIProvenance] - a build system that attested the same thing;
//   - [BindingHistory] - SafeLane's own record of having released it;
//   - [BindingConfirmedBaseline] - a person said so, once, and SafeLane checked
//     the commit exists in the registered repository before believing them.
//
// There is no fifth place, and in particular there is no inference. A tag
// spelled `sha-abc123` is a string. An image pushed a minute after a commit
// landed is an image pushed a minute after a commit landed. Two digests that
// share a prefix share a prefix. None of those is a fact about what was built,
// and [Resolver] will not treat them as one - an Artifact whose provenance
// cannot be established is an honest failure, and honest failures are the
// entire point of the package.
//
// # Finding the Artifact for a revision
//
// [Resolver.FindRevision] enumerates the repository's tags, resolves each to a
// digest, and reads the source metadata of each - then returns the one whose
// verified revision matches. When nothing matches it says so and asks for an
// exact reference. It never falls back to "the closest one" or "the most
// recent one", because deploying an older container than the one asked for is
// the specific accident this package exists to prevent.
package oci
