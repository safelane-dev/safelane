package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Risk is an assessment outcome. It is not a number and it is not computed
// here: an assessment reports one of these three, and [Policy.RiskMapping]
// turns it into a lane the operator already declared.
type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// Risks are the three risk levels, in ascending order. Every configuration
// must map all three, because a level with no lane would silently fall back to
// the default and hide a configuration mistake behind a working release.
var Risks = []Risk{RiskLow, RiskMedium, RiskHigh}

// Impact is how much a given Environment matters. It is the operator's
// statement about consequences, made once at registration, and it is an input
// to assessment rather than an output of it.
type Impact string

const (
	ImpactLow         Impact = "low"
	ImpactSignificant Impact = "significant"
	ImpactCritical    Impact = "critical"
)

// Impacts are the three impact levels, in ascending order.
var Impacts = []Impact{ImpactLow, ImpactSignificant, ImpactCritical}

// Config is the whole of `safelane.yml`.
type Config struct {
	Application  Application   `yaml:"application"`
	Artifact     Artifact      `yaml:"artifact"`
	Environments []Environment `yaml:"environments"`
	Policy       Policy        `yaml:"policy"`
}

// Application identifies what is being released and where its source lives.
type Application struct {
	// Name is the Application's SafeLane name. It is also a path segment, so
	// it must be a single safe one.
	Name string `yaml:"name"`
	// Repository is the GitHub repository as `owner/name`, read from the Git
	// origin at registration.
	Repository string `yaml:"repository"`
}

// Artifact identifies the one container SafeLane releases.
type Artifact struct {
	// Container is the name of the container inside the Rollout's pod
	// template. The Release Patch replaces that container's image and nothing
	// else, so the wrong name here is the difference between releasing the
	// application and releasing its sidecar.
	Container string `yaml:"container"`
	// Image is the image repository with no tag and no digest, derived at
	// registration by stripping the reference off the live image. A tag here
	// would be a mutable pointer, and SafeLane releases digests.
	Image string `yaml:"image"`
}

// Environment is one place the Application can be released to.
type Environment struct {
	// Name is the Environment's SafeLane name, and a path segment.
	Name string `yaml:"name"`
	// Impact is how much this Environment matters.
	Impact Impact `yaml:"impact"`
	// Kubernetes is where the Rollout lives.
	Kubernetes Kubernetes `yaml:"kubernetes"`
}

// Kubernetes is the Deployment Target: the caller context, the namespace, and
// the one Rollout SafeLane may patch.
type Kubernetes struct {
	Context   string `yaml:"context"`
	Namespace string `yaml:"namespace"`
	Rollout   string `yaml:"rollout"`
}

// Policy is the operator's block. Registration writes it once, from the
// compiled defaults, and never touches it again: [Reconcile] carries whatever
// is here across byte-for-byte.
type Policy struct {
	// DefaultLane is used when there is no risk at all - no assessment, or one
	// that could not be validated. It is the cautious answer, never the widest
	// lane, and "no assessment" is an expected case rather than a defect.
	DefaultLane string `yaml:"default_lane"`
	// RiskMapping maps each of the three risk levels to a declared lane.
	RiskMapping map[Risk]string `yaml:"risk_mapping"`
	// Lanes are every rollout envelope the operator allows, keyed by name.
	Lanes map[string]Lane `yaml:"lanes"`
}

// Lane is one rollout envelope: the ordered traffic weights a release passes
// through. All but the last become explicit canary steps with a gate after
// each; the last is reached once the Rollout runs out of steps. N weights
// therefore make N-1 gates, never N.
type Lane struct {
	Weights []int `yaml:"weights"`
}

// Environment returns the Environment with the given name.
func (c Config) Environment(name string) (Environment, bool) {
	for _, env := range c.Environments {
		if env.Name == name {
			return env, true
		}
	}
	return Environment{}, false
}

// EnvironmentNames returns every configured Environment name, in file order.
func (c Config) EnvironmentNames() []string {
	names := make([]string, 0, len(c.Environments))
	for _, env := range c.Environments {
		names = append(names, env.Name)
	}
	return names
}

// LaneFor resolves a risk level to the lane a release should run in. An empty
// or unrecognised risk resolves to DefaultLane, because "no assessment
// available" is a legitimate case that must still pick a cautious lane rather
// than fail or widen.
func (p Policy) LaneFor(risk Risk) (name string, lane Lane, err error) {
	name = p.RiskMapping[risk]
	if name == "" {
		name = p.DefaultLane
	}
	lane, ok := p.Lanes[name]
	if !ok {
		return "", Lane{}, release.Invalid("undeclared_lane", "policy.lanes",
			fmt.Sprintf("lane %q is not declared under policy.lanes", name),
			"Register this application again to restore the default lanes.")
	}
	return name, lane, nil
}

// LaneNames returns every declared lane name, sorted.
func (p Policy) LaneNames() []string {
	names := make([]string, 0, len(p.Lanes))
	for name := range p.Lanes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultPolicy is the policy block registration writes for a new Application:
// three lanes, and the obvious mapping onto them. Registration compiles these
// once and then never revisits them, so an operator who edits a weight keeps
// that edit through every later registration.
func DefaultPolicy() Policy {
	return Policy{
		DefaultLane: "guarded",
		RiskMapping: map[Risk]string{
			RiskLow:    "fast",
			RiskMedium: "standard",
			RiskHigh:   "guarded",
		},
		Lanes: map[string]Lane{
			"fast":     {Weights: []int{50, 100}},
			"standard": {Weights: []int{25, 50, 100}},
			"guarded":  {Weights: []int{25, 50, 75, 100}},
		},
	}
}

// Validate reports every problem at once rather than the first one, because a
// person fixing a configuration file would rather see the whole list than
// discover it one save at a time.
func (c Config) Validate() error {
	var errs release.Errors

	errs = append(errs, c.validateApplication()...)
	errs = append(errs, c.validateArtifact()...)
	errs = append(errs, c.validateEnvironments()...)
	errs = append(errs, c.Policy.validate()...)

	return errs.OrNil()
}

func (c Config) validateApplication() release.Errors {
	var errs release.Errors
	name := strings.TrimSpace(c.Application.Name)
	if name == "" {
		errs = append(errs, missingField("application.name", "no application name was configured",
			"Register this application again."))
	} else if err := checkPathSegment("application.name", name); err != nil {
		errs = append(errs, err)
	}

	repo := strings.TrimSpace(c.Application.Repository)
	switch {
	case repo == "":
		errs = append(errs, missingField("application.repository", "no source repository was configured",
			"Register this application again from a checkout with a GitHub origin."))
	case strings.Count(repo, "/") != 1, strings.HasPrefix(repo, "/"), strings.HasSuffix(repo, "/"):
		errs = append(errs, release.Invalid("invalid_repository", "application.repository",
			fmt.Sprintf("%q is not a GitHub repository", repo),
			"Write the repository as owner/name."))
	}
	return errs
}

func (c Config) validateArtifact() release.Errors {
	var errs release.Errors
	if strings.TrimSpace(c.Artifact.Container) == "" {
		errs = append(errs, missingField("artifact.container", "no container was configured",
			"Register this application again and choose the container to release."))
	}

	image := strings.TrimSpace(c.Artifact.Image)
	switch {
	case image == "":
		errs = append(errs, missingField("artifact.image", "no image repository was configured",
			"Register this application again."))
	case strings.Contains(image, "@"):
		errs = append(errs, release.Invalid("image_is_a_reference", "artifact.image",
			fmt.Sprintf("%q pins a digest; this is the image repository, not one image", image),
			"Remove everything from the @ onwards."))
	case hasTag(image):
		errs = append(errs, release.Invalid("image_is_a_reference", "artifact.image",
			fmt.Sprintf("%q carries a tag; this is the image repository, not one image", image),
			"Remove the :tag suffix."))
	}
	return errs
}

// hasTag reports whether an image repository carries a `:tag` suffix. A colon
// inside a registry host is a port, not a tag, so only the last path segment
// counts.
func hasTag(image string) bool {
	lastSlash := strings.LastIndex(image, "/")
	return strings.Contains(image[lastSlash+1:], ":")
}

func (c Config) validateEnvironments() release.Errors {
	var errs release.Errors
	if len(c.Environments) == 0 {
		errs = append(errs, missingField("environments", "no environments were configured",
			"Register this application again and choose an environment."))
		return errs
	}

	seen := make(map[string]int, len(c.Environments))
	for i, env := range c.Environments {
		field := fmt.Sprintf("environments[%d]", i)
		name := strings.TrimSpace(env.Name)
		switch {
		case name == "":
			errs = append(errs, missingField(field+".name", "an environment has no name",
				"Give every environment a name."))
		default:
			if err := checkPathSegment(field+".name", name); err != nil {
				errs = append(errs, err)
			}
			if first, dup := seen[name]; dup {
				errs = append(errs, release.Invalid("duplicate_environment", field+".name",
					fmt.Sprintf("environment %q is configured twice, at entries %d and %d", name, first, i),
					"Give each environment a distinct name, or remove the duplicate entry."))
			} else {
				seen[name] = i
			}
		}

		if !validImpact(env.Impact) {
			errs = append(errs, release.Invalid("invalid_impact", field+".impact",
				fmt.Sprintf("%q is not an impact level", env.Impact),
				"Set impact to one of "+joinImpacts()+"."))
		}

		if strings.TrimSpace(env.Kubernetes.Context) == "" {
			errs = append(errs, missingField(field+".kubernetes.context", "no kubectl context was configured",
				"Register this application again from the context that can read the Rollout."))
		}
		if strings.TrimSpace(env.Kubernetes.Namespace) == "" {
			errs = append(errs, missingField(field+".kubernetes.namespace", "no namespace was configured",
				"Register this application again."))
		}
		if strings.TrimSpace(env.Kubernetes.Rollout) == "" {
			errs = append(errs, missingField(field+".kubernetes.rollout", "no rollout was configured",
				"Register this application again and choose the Rollout to release."))
		}
	}
	return errs
}

func (p Policy) validate() release.Errors {
	var errs release.Errors

	if len(p.Lanes) == 0 {
		errs = append(errs, missingField("policy.lanes", "no lanes were declared",
			"Register this application again to restore the default lanes."))
	}
	for _, name := range p.LaneNames() {
		errs = append(errs, p.Lanes[name].validate("policy.lanes."+name, name)...)
	}

	switch {
	case strings.TrimSpace(p.DefaultLane) == "":
		errs = append(errs, missingField("policy.default_lane", "no default_lane was configured",
			"Set default_lane to the most cautious declared lane."))
	default:
		if _, ok := p.Lanes[p.DefaultLane]; !ok {
			errs = append(errs, undeclaredLane("policy.default_lane", p.DefaultLane))
		}
	}

	for _, risk := range Risks {
		lane, ok := p.RiskMapping[risk]
		if !ok || strings.TrimSpace(lane) == "" {
			errs = append(errs, missingField("policy.risk_mapping."+string(risk),
				fmt.Sprintf("risk level %q maps to no lane", risk),
				"Map every one of "+joinRisks()+" to a declared lane."))
			continue
		}
		if _, declared := p.Lanes[lane]; !declared {
			errs = append(errs, undeclaredLane("policy.risk_mapping."+string(risk), lane))
		}
	}
	for risk := range p.RiskMapping {
		if !validRisk(risk) {
			errs = append(errs, release.Invalid("unknown_risk_level", "policy.risk_mapping."+string(risk),
				fmt.Sprintf("%q is not a risk level", risk),
				"Map only "+joinRisks()+"."))
		}
	}

	return errs
}

// validate checks the two properties that make a lane a rollout envelope
// rather than an arbitrary list: it only ever moves forward, and it finishes.
func (l Lane) validate(field, name string) release.Errors {
	var errs release.Errors
	if len(l.Weights) == 0 {
		errs = append(errs, missingField(field+".weights",
			fmt.Sprintf("lane %q declares no weights", name),
			"Give every lane at least one weight, ending at 100."))
		return errs
	}

	previous := 0
	for i, weight := range l.Weights {
		if weight < 1 || weight > 100 {
			errs = append(errs, release.Invalid("weight_out_of_range", fmt.Sprintf("%s.weights[%d]", field, i),
				fmt.Sprintf("lane %q has a weight of %d", name, weight),
				"Every weight is a traffic percentage between 1 and 100."))
			continue
		}
		if weight <= previous {
			errs = append(errs, release.Invalid("weights_not_increasing", fmt.Sprintf("%s.weights[%d]", field, i),
				fmt.Sprintf("lane %q goes from %d to %d", name, previous, weight),
				"Each weight must be larger than the one before it; a lane only ever sends more traffic."))
		}
		previous = weight
	}

	if last := l.Weights[len(l.Weights)-1]; last != 100 {
		errs = append(errs, release.Invalid("lane_does_not_finish", field+".weights",
			fmt.Sprintf("lane %q ends at %d%%", name, last),
			"End every lane at 100, so a release that passes every gate is fully live."))
	}
	return errs
}

// Gates is the number of times a release in this lane stops for a decision:
// one fewer than its weights, because the final weight is reached by running
// out of steps rather than by passing one.
func (l Lane) Gates() int {
	if len(l.Weights) == 0 {
		return 0
	}
	return len(l.Weights) - 1
}

func missingField(field, message, remedy string) *release.Error {
	return release.Invalid("missing_config_field", field, message, remedy)
}

func undeclaredLane(field, lane string) *release.Error {
	return release.Invalid("undeclared_lane", field,
		fmt.Sprintf("%q is not declared under policy.lanes", lane),
		"Name a lane that exists, or register this application again to restore the defaults.")
}

// checkPathSegment rejects anything that would escape, or reshape, the derived
// directory layout. Application and Environment names become path segments, so
// this is a boundary rather than a style rule.
func checkPathSegment(field, value string) *release.Error {
	switch {
	case value == "." || value == "..":
		break
	case strings.ContainsAny(value, `/\`):
		break
	case strings.ContainsRune(value, ':'):
		break
	default:
		return nil
	}
	return release.Invalid("unsafe_name", field,
		fmt.Sprintf("%q cannot be used as a directory name", value),
		"Use a plain name with no slashes, colons, or dots on their own.")
}

func validImpact(impact Impact) bool {
	for _, known := range Impacts {
		if impact == known {
			return true
		}
	}
	return false
}

func validRisk(risk Risk) bool {
	for _, known := range Risks {
		if risk == known {
			return true
		}
	}
	return false
}

func joinImpacts() string {
	names := make([]string, 0, len(Impacts))
	for _, impact := range Impacts {
		names = append(names, string(impact))
	}
	return strings.Join(names, ", ")
}

func joinRisks() string {
	names := make([]string, 0, len(Risks))
	for _, risk := range Risks {
		names = append(names, string(risk))
	}
	return strings.Join(names, ", ")
}
