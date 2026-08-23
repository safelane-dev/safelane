package discovery

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Service reads a cluster and a Git checkout. Both are injected seams, so the
// whole package runs against canned output in tests.
type Service struct {
	// Run is how kubectl is called. Nil means the real binary.
	Run Runner
	// Origin returns the GitHub repository for a checkout, as `owner/name`.
	// Nil means read `git remote get-url origin`.
	Origin func(root string) (string, error)
}

// Discovery is everything `safelane discover <namespace>` found.
type Discovery struct {
	// Namespace is the one namespace that was read. Discovery never scans a
	// cluster, so this is the entire search.
	Namespace string `json:"namespace"`
	// Context is the kubectl context the read went through. It is reported so
	// a person can see which cluster answered, and it is never changed.
	Context string `json:"context"`
	// Repository is the GitHub repository of the current checkout, or empty
	// when there is no GitHub origin.
	Repository string `json:"repository,omitempty"`
	// Rollouts is every readable Rollout in the namespace, in name order.
	Rollouts []Rollout `json:"rollouts"`
}

// Rollout is one Rollout as it appeared in the namespace listing.
type Rollout struct {
	Name string `json:"name"`
	// Containers are the inline containers in the pod template, read
	// dynamically. A Rollout with none is reported with none, not skipped.
	Containers []Container `json:"containers"`
	// Environment says whether SafeLane could release this Rollout, and why
	// not when it could not.
	Environment Compatibility `json:"environment"`
}

// Container is one inline container and the image it is running.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// Compatibility is a yes-or-no with its reasons attached. The reasons are
// written to be read out loud.
type Compatibility struct {
	Supported bool     `json:"supported"`
	Reasons   []Reason `json:"reasons,omitempty"`
}

// Reason is one thing that is wrong, with a stable code for the machine path
// and a sentence for the person.
type Reason struct {
	Code        string `json:"code"`
	Explanation string `json:"explanation"`
}

// Target is the full read of one chosen Rollout: the Rollout itself, the
// Services it points at, and the background analysis it references.
type Target struct {
	Namespace string `json:"namespace"`
	Context   string `json:"context"`
	Rollout   string `json:"rollout"`

	Containers []Container `json:"containers"`
	// StableService and CanaryService are the names the Rollout gave.
	StableService string `json:"stable_service"`
	CanaryService string `json:"canary_service"`
	// Analysis is every background analysis the Rollout references. SafeLane
	// watches these; it never writes one.
	Analysis []AnalysisReference `json:"analysis"`

	// Environment and Artifact are the two separate answers. An application
	// can pass one and fail the other, and being told only "incompatible"
	// would hide which.
	Environment Compatibility `json:"environment"`
	Artifact    Compatibility `json:"artifact"`

	// Repository is the GitHub repository of the current checkout.
	Repository string `json:"repository,omitempty"`

	// Fingerprint covers exactly the facts registration depends on.
	Fingerprint string `json:"fingerprint"`
}

// AnalysisReference is one background AnalysisTemplate the Rollout names.
type AnalysisReference struct {
	Name string `json:"name"`
	// Provider is the metric provider the template asks, title-cased for
	// display ("Prometheus", "Datadog"). Empty when the template could not be
	// read.
	Provider string `json:"provider,omitempty"`
	// Resolved is whether the template actually exists and could be read.
	Resolved bool `json:"resolved"`
}

// SelectedContainer returns the container with the given name.
func (t Target) SelectedContainer(name string) (Container, bool) {
	for _, container := range t.Containers {
		if container.Name == name {
			return container, true
		}
	}
	return Container{}, false
}

// Discover lists every readable Rollout in one namespace, with the inline
// containers and images each is running.
//
// It reports the current kubectl context and never changes it. There is no
// cluster scan and no second namespace: the caller says where to look.
func (s Service) Discover(ctx context.Context, root, namespace string) (Discovery, error) {
	if strings.TrimSpace(namespace) == "" {
		return Discovery{}, release.Invalid("missing_namespace", "namespace",
			"no namespace was given",
			"Say which namespace the application runs in.")
	}

	current, err := s.currentContext(ctx)
	if err != nil {
		return Discovery{}, err
	}

	list, err := getJSON[rolloutList](ctx, s.runner(),
		[]string{"get", "rollouts.argoproj.io", "-n", namespace, "-o", "json"})
	if err != nil {
		return Discovery{}, unreachable(namespace, err)
	}

	found := Discovery{Namespace: namespace, Context: current, Rollouts: []Rollout{}}
	found.Repository, _ = s.origin(root)

	for _, doc := range list.Items {
		found.Rollouts = append(found.Rollouts, Rollout{
			Name:        doc.Metadata.Name,
			Containers:  containersOf(doc),
			Environment: environmentCompatibility(doc),
		})
	}
	sort.Slice(found.Rollouts, func(i, j int) bool { return found.Rollouts[i].Name < found.Rollouts[j].Name })
	return found, nil
}

// Inspect reads one chosen Rollout in full: the Rollout, the Services it names,
// and every background analysis it references.
func (s Service) Inspect(ctx context.Context, root, namespace, rollout string) (Target, error) {
	current, err := s.currentContext(ctx)
	if err != nil {
		return Target{}, err
	}

	doc, err := getJSON[rolloutDoc](ctx, s.runner(),
		[]string{"get", "rollouts.argoproj.io", rollout, "-n", namespace, "-o", "json"})
	if err != nil {
		return Target{}, release.Invalid("rollout_not_readable", "rollout",
			fmt.Sprintf("could not read Rollout %q in namespace %s: %v", rollout, namespace, err),
			"Check the name, and that the current context can read Rollouts in that namespace.")
	}

	target := Target{
		Namespace:  namespace,
		Context:    current,
		Rollout:    doc.Metadata.Name,
		Containers: containersOf(doc),
	}
	target.Repository, _ = s.origin(root)

	compat := environmentCompatibility(doc)
	if doc.Spec.Strategy.Canary != nil {
		target.StableService = doc.Spec.Strategy.Canary.StableService
		target.CanaryService = doc.Spec.Strategy.Canary.CanaryService
		compat.Reasons = append(compat.Reasons, s.resolveServices(ctx, namespace, doc)...)
		target.Analysis, compat.Reasons = s.resolveAnalysis(ctx, namespace, doc, compat.Reasons)
	}
	compat.Supported = len(compat.Reasons) == 0
	target.Environment = compat
	target.Artifact = artifactCompatibility(target)
	target.Fingerprint = fingerprint(target)
	return target, nil
}

func (s Service) resolveServices(ctx context.Context, namespace string, doc rolloutDoc) []Reason {
	var reasons []Reason
	for label, name := range map[string]string{
		"stable": doc.Spec.Strategy.Canary.StableService,
		"canary": doc.Spec.Strategy.Canary.CanaryService,
	} {
		if name == "" {
			continue
		}
		if _, err := s.runner()(ctx, []string{"get", "service", name, "-n", namespace, "-o", "json"}); err != nil {
			reasons = append(reasons, Reason{
				Code: "service_not_resolved",
				Explanation: fmt.Sprintf("The Rollout names %q as its %s Service, but that Service could not be read in namespace %s.",
					name, label, namespace),
			})
		}
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].Explanation < reasons[j].Explanation })
	return reasons
}

func (s Service) resolveAnalysis(ctx context.Context, namespace string, doc rolloutDoc, reasons []Reason) ([]AnalysisReference, []Reason) {
	analysis := doc.Spec.Strategy.Canary.Analysis
	if analysis == nil {
		return nil, reasons
	}
	refs := make([]AnalysisReference, 0, len(analysis.Templates))
	for _, ref := range analysis.Templates {
		found := AnalysisReference{Name: ref.TemplateName}
		kind := "analysistemplate"
		if ref.ClusterScope {
			kind = "clusteranalysistemplate"
		}
		args := []string{"get", kind, ref.TemplateName, "-o", "json"}
		if !ref.ClusterScope {
			args = append(args, "-n", namespace)
		}
		if template, err := getJSON[analysisTemplateDoc](ctx, s.runner(), args); err != nil {
			reasons = append(reasons, Reason{
				Code: "analysis_template_not_resolved",
				Explanation: fmt.Sprintf("The Rollout's background analysis references %q, but that AnalysisTemplate could not be read in namespace %s.",
					ref.TemplateName, namespace),
			})
		} else {
			found.Resolved = true
			found.Provider = providerOf(template)
		}
		refs = append(refs, found)
	}
	return refs, reasons
}

// environmentCompatibility names every shape SafeLane cannot release, in the
// order a person would hit them.
func environmentCompatibility(doc rolloutDoc) Compatibility {
	var reasons []Reason

	if owner, tool := gitOpsOwner(doc.Metadata); tool != "" {
		reasons = append(reasons, Reason{
			Code: "managed_by_gitops",
			Explanation: fmt.Sprintf("%s already manages this Rollout (%s). SafeLane would patch the cluster and %s would put it back, so it does not take over resources another tool owns.",
				tool, owner, tool),
		})
	}

	switch {
	case doc.Spec.Strategy.BlueGreen != nil:
		reasons = append(reasons, Reason{
			Code:        "blue_green_strategy",
			Explanation: "This Rollout uses a blue-green strategy. SafeLane releases in canary steps, where a share of real traffic decides whether to continue.",
		})
	case doc.Spec.Strategy.Canary == nil:
		reasons = append(reasons, Reason{
			Code:        "no_canary_strategy",
			Explanation: "This Rollout declares no canary strategy, so there are no traffic steps for SafeLane to widen or stop.",
		})
	}

	if doc.Spec.WorkloadRef != nil {
		reasons = append(reasons, Reason{
			Code: "workload_reference",
			Explanation: fmt.Sprintf("This Rollout takes its pod template from %s %q rather than declaring it inline, so the image SafeLane would change does not live on the Rollout.",
				doc.Spec.WorkloadRef.Kind, doc.Spec.WorkloadRef.Name),
		})
	} else if len(doc.Spec.Template.Spec.Containers) == 0 {
		reasons = append(reasons, Reason{
			Code:        "no_inline_container",
			Explanation: "This Rollout's pod template declares no container, so there is no image for SafeLane to release.",
		})
	}

	if canary := doc.Spec.Strategy.Canary; canary != nil {
		if canary.Analysis == nil || len(canary.Analysis.Templates) == 0 {
			reasons = append(reasons, Reason{
				Code:        "no_background_analysis",
				Explanation: "This Rollout runs no background analysis. SafeLane widens a release only after a fresh healthy measurement, and without one there is nothing to wait for.",
			})
		}
		for _, code := range unsupportedStepKinds(canary.Steps) {
			reasons = append(reasons, Reason{
				Code:        "unsupported_step",
				Explanation: fmt.Sprintf("This Rollout's canary steps include %q. SafeLane rewrites the steps when it releases, and it will not rewrite a step whose meaning it does not carry across.", code),
			})
		}
	}

	return Compatibility{Supported: len(reasons) == 0, Reasons: reasons}
}

// supportedStepKeys are the two step shapes SafeLane can regenerate from a
// lane. Anything else is preserved by refusing rather than by guessing.
var supportedStepKeys = map[string]bool{"setWeight": true, "pause": true}

func unsupportedStepKinds(steps []canaryStep) []string {
	seen := map[string]bool{}
	var kinds []string
	for _, step := range steps {
		for key := range step {
			if supportedStepKeys[key] || seen[key] {
				continue
			}
			seen[key] = true
			kinds = append(kinds, key)
		}
	}
	sort.Strings(kinds)
	return kinds
}

// gitOpsTracking are the labels and annotations Argo CD and Flux stamp on what
// they own. A resource carrying one is continuously reconciled from Git, so a
// patch SafeLane applied would be reverted without warning.
var gitOpsTracking = []struct{ key, tool string }{
	{"argocd.argoproj.io/instance", "Argo CD"},
	{"argocd.argoproj.io/tracking-id", "Argo CD"},
	{"kustomize.toolkit.fluxcd.io/name", "Flux"},
	{"helm.toolkit.fluxcd.io/name", "Flux"},
}

func gitOpsOwner(meta objectMeta) (owner, tool string) {
	for _, marker := range gitOpsTracking {
		if marker.tool == "" {
			continue
		}
		if value, ok := meta.Labels[marker.key]; ok && value != "" {
			return value, marker.tool
		}
		if value, ok := meta.Annotations[marker.key]; ok && value != "" {
			return value, marker.tool
		}
	}
	return "", ""
}

// artifactCompatibility answers the second question: could SafeLane trace what
// is running back to the source it came from?
//
// This is a readiness check made without touching a registry - the actual
// provenance lookup happens at release time. It reports the three things a
// trace needs to be possible at all:
//
//   - a GitHub repository for the checkout, or there is no source history to
//     compare a candidate against;
//   - an image reference that names a registry repository, or there is nothing
//     to ask;
//   - an immutable digest, or the thing running today is whatever that tag
//     pointed at when the pod started, which is not a fact SafeLane can carry
//     into a comparison.
//
// The last one is the common honest failure. A Rollout running `:blue` is a
// perfectly good Argo canary and a perfectly bad starting point for "what is
// deployed right now", which is why the answer is separate from the Kubernetes
// one instead of collapsed into a single verdict.
func artifactCompatibility(t Target) Compatibility {
	var reasons []Reason
	if strings.TrimSpace(t.Repository) == "" {
		reasons = append(reasons, Reason{
			Code:        "no_github_origin",
			Explanation: "This checkout has no GitHub origin, so there is no source history to compare a candidate against.",
		})
	}
	if len(t.Containers) == 0 {
		reasons = append(reasons, Reason{
			Code:        "no_image_to_trace",
			Explanation: "There is no inline container, so there is no image to trace back to a commit.",
		})
	}
	for _, container := range t.Containers {
		switch {
		case !strings.Contains(container.Image, "/"):
			reasons = append(reasons, Reason{
				Code: "image_has_no_repository",
				Explanation: fmt.Sprintf("Container %q runs %q, which names no registry repository, so SafeLane cannot ask a registry what it was built from.",
					container.Name, container.Image),
			})
		case !strings.Contains(container.Image, "@sha256:"):
			reasons = append(reasons, Reason{
				Code: "image_is_a_mutable_tag",
				Explanation: fmt.Sprintf("Container %q runs %q, a tag rather than a digest, so SafeLane cannot tell which build is live from the Rollout alone. It will ask you once which commit is deployed, and keep exact baselines from then on.",
					container.Name, container.Image),
			})
		}
	}
	return Compatibility{Supported: len(reasons) == 0, Reasons: reasons}
}

// ImageRepository strips the tag or digest off a live image reference, leaving
// the repository. That is what registration stores: a tag is a mutable
// pointer, and SafeLane releases digests.
func ImageRepository(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	lastSlash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > lastSlash {
		image = image[:colon]
	}
	return image
}

func containersOf(doc rolloutDoc) []Container {
	containers := make([]Container, 0, len(doc.Spec.Template.Spec.Containers))
	for _, c := range doc.Spec.Template.Spec.Containers {
		containers = append(containers, Container{Name: c.Name, Image: c.Image})
	}
	return containers
}

// providerOf names the metric provider a template asks, so registration can
// say "checked by Prometheus" rather than "checked somehow".
func providerOf(template analysisTemplateDoc) string {
	for _, metric := range template.Spec.Metrics {
		names := make([]string, 0, len(metric.Provider))
		for name := range metric.Provider {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 0 {
			return displayProvider(names[0])
		}
	}
	return ""
}

var providerNames = map[string]string{
	"prometheus": "Prometheus",
	"datadog":    "Datadog",
	"newRelic":   "New Relic",
	"wavefront":  "Wavefront",
	"cloudWatch": "CloudWatch",
	"graphite":   "Graphite",
	"influxdb":   "InfluxDB",
	"job":        "a Kubernetes Job",
	"web":        "an HTTP endpoint",
	"kayenta":    "Kayenta",
	"skywalking": "SkyWalking",
}

func displayProvider(name string) string {
	if display, ok := providerNames[name]; ok {
		return display
	}
	return name
}

func (s Service) runner() Runner {
	if s.Run != nil {
		return s.Run
	}
	return RealRunner
}

// currentContext reports which context the reads went through. It is a read
// like every other call here: nothing in this package selects a context.
func (s Service) currentContext(ctx context.Context) (string, error) {
	out, err := s.runner()(ctx, []string{"config", "current-context"})
	if err != nil {
		return "", release.Invalid("no_kubectl_context", "kubernetes",
			"kubectl has no current context",
			"Point kubectl at the cluster the application runs in, then try again.")
	}
	return strings.TrimSpace(string(out)), nil
}

func (s Service) origin(root string) (string, error) {
	if s.Origin != nil {
		return s.Origin(root)
	}
	return GitHubOrigin(root)
}

// GitHubOrigin reads `git remote get-url origin` and reduces it to owner/name.
func GitHubOrigin(root string) (string, error) {
	cmd := exec.Command("git", "-C", root, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read git origin: %w", err)
	}
	return ParseGitHubRemote(strings.TrimSpace(string(out)))
}

// ParseGitHubRemote reduces an SSH or HTTPS GitHub remote to `owner/name`.
func ParseGitHubRemote(url string) (string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	switch {
	case strings.HasPrefix(trimmed, "git@github.com:"):
		trimmed = strings.TrimPrefix(trimmed, "git@github.com:")
	case strings.Contains(trimmed, "github.com/"):
		trimmed = trimmed[strings.Index(trimmed, "github.com/")+len("github.com/"):]
	default:
		return "", fmt.Errorf("%q is not a GitHub remote", url)
	}
	if strings.Count(trimmed, "/") != 1 || strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") {
		return "", fmt.Errorf("%q is not a GitHub remote", url)
	}
	return trimmed, nil
}

func unreachable(namespace string, err error) error {
	return release.Invalid("namespace_not_readable", "namespace",
		fmt.Sprintf("could not read Rollouts in namespace %s: %v", namespace, err),
		"Check the namespace name, and that the current context can list Rollouts there.")
}
