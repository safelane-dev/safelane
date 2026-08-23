// Package config owns the one file SafeLane reads and every path it derives.
//
// There is exactly one configuration file per Application,
// `~/.safelane/apps/<application>/safelane.yml`, and it holds only what a person
// answered during registration: which Application this is, which container image
// it ships, which Environments it can be released to, and which lanes are
// allowed. Everything else SafeLane once kept here - a schema version, a
// credential path, a default branch, a required check name, an image tag
// pattern, a template path, and heuristic thresholds - is discovered, derived,
// or gone. See docs/plans/41.md decision 2.
//
// # Why one file and no schema version
//
// A schema version is a promise to keep reading what an older version wrote.
// SafeLane makes the opposite promise: configuration written by an earlier
// version is ignored, never migrated, and never deleted. [Load] says so once, in
// one sentence, and points at registration. Nothing in this package removes a
// file from a user's home directory, so an operator who was mid-migration keeps
// whatever they had.
//
// # Derived, not configured
//
// [ForApp] and [Locations.ForEnvironment] derive all four locations from the
// Application and Environment names alone:
//
//	~/.safelane/apps/<application>/safelane.yml
//	~/.safelane/apps/<application>/environments/<environment>/identities/controller/kubeconfig
//	~/.safelane/apps/<application>/environments/<environment>/releases/
//	~/.safelane/apps/<application>/environments/<environment>/history.jsonl
//
// The controller kubeconfig path in particular is derived and never read from
// YAML. A path in a configuration file is a path someone can point somewhere
// else; the privileged identity's location is not a setting.
//
// Because those names become path segments, [Config.Validate] rejects any name
// that is not a single safe segment. That check is a path-traversal boundary,
// not cosmetics.
//
// # Rewriting is safe to repeat
//
// Registration runs more than once - a namespace moves, a second Environment
// appears, someone re-runs it to check. [Reconcile] therefore takes the file
// that is already there and returns the bytes that should replace it:
//
//   - the discovered blocks (application, artifact, environments) are
//     regenerated from what was observed;
//   - the policy block is carried across byte-for-byte, comments and all,
//     because the operator wrote it and discovery has no opinion about it;
//   - an Environment is matched by name, so re-registering `production`
//     replaces that entry and leaves `staging` untouched.
//
// [Write] then compares and writes only if the bytes differ, through a
// temporary file and a rename, so an unchanged registration writes nothing at
// all and an interrupted one cannot leave a half-written file behind.
package config
