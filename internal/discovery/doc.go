// Package discovery reads what is already running and reports what SafeLane can
// do with it.
//
// It provisions nothing, creates nothing, and changes nothing - not a
// Kubernetes object, and not the current kubectl context. Every call is a read
// against one namespace, through the kubectl binary the user is already
// authenticated with, and the whole package's output is a description.
//
// # Two separate questions
//
// A running application can be compatible with SafeLane's Kubernetes half and
// not with its Artifact half, and telling a person "incompatible" would hide
// which. So [Target] answers them separately:
//
//   - [Target.Environment] - is there an Argo canary Rollout, with an inline
//     container, resolvable stable and canary Services, and a background
//     analysis reference?
//   - [Target.Artifact] - can the running image be traced back to the source it
//     was built from?
//
// The official argoproj/rollouts-demo Istio example passes the first and fails
// the second until its image is republished with provenance. That is a useful,
// true thing to be able to say, and it is only sayable because the two are
// reported apart.
//
// # Explaining rather than failing
//
// Blue-green, `workloadRef`, a pod template with no inline container, analysis
// that only runs inline between steps, a Service or AnalysisTemplate that does
// not resolve, step types SafeLane would have to understand to preserve, and
// resources Argo CD or Flux already own: each of these is detected by name and
// explained in a sentence a person can act on. None of them is an error, a
// stack trace, or a silent empty list. SafeLane does not support them, and
// says which one it found.
//
// # Fingerprints
//
// [Target.Fingerprint] is a hash of exactly the facts registration depends on.
// Registration re-reads and re-fingerprints before it writes, so a namespace
// that moved between "here is what I found" and "yes, register that" is
// refused rather than written down wrong. It is not a security boundary - it is
// the difference between registering what the user saw and registering what
// happened to be there afterwards.
package discovery
