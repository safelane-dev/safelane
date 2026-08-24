// Package releasepatch builds the narrowest mutation SafeLane is capable of.
//
// Two paths change and nothing else does:
//
//	/spec/template/spec/containers/<selected-index>/image
//	/spec/strategy/canary/steps
//
// Probes, resources, replicas, environment, secrets, ports, Services, the
// traffic router, and the background analysis all come out byte-for-byte
// identical. That is not a promise about how carefully the code was written -
// it is a property of a JSON Patch with exactly two replace operations, and
// [Verify] checks it against the real before-and-after.
//
// # Why a patch and not a rendered manifest
//
// SafeLane used to render whole Kubernetes objects from a template it owned,
// which meant every field in the Rollout was SafeLane's opinion whether it
// meant to have one or not. A user who set a resource limit by hand would
// lose it on the next release, and the only defence was remembering to
// template every field somebody might care about.
//
// A patch inverts that. The fields SafeLane can affect are the fields it names,
// and it names two. Anything changed on the rest of the Rollout
// survives, including things SafeLane has never heard of.
//
// # Tests, in the JSON Patch sense
//
// The operations begin with `test` operations on the resource version and the
// current image. If either moved between reading and applying, the whole patch
// is rejected by the API server rather than partly applied. That is the
// difference between "SafeLane checked first" and "SafeLane cannot overwrite
// something it did not see" - the second one holds even when two SafeLanes run
// at once.
package releasepatch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Operation is one JSON Patch operation (RFC 6902).
type Operation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Patch is the exact change SafeLane intends, and everything needed to check
// that the cluster still looks the way it did when the change was decided.
type Patch struct {
	RolloutUID      string `json:"rollout_uid"`
	ResourceVersion string `json:"resource_version"`
	Container       string `json:"container"`
	// ContainerIndex is where that container sits in the pod template. It is
	// found by name at read time, so a container list that was reordered does
	// not silently move the patch onto a sidecar.
	ContainerIndex int    `json:"container_index"`
	PreviousImage  string `json:"previous_image"`
	CandidateImage string `json:"candidate_image"`
	// PreviousSteps is the canary steps as they were, kept so proof can show
	// what was replaced rather than only what replaced it.
	PreviousSteps json.RawMessage `json:"previous_steps,omitempty"`
	Lane          string          `json:"lane"`
	Weights       []int           `json:"weights"`
	Operations    []Operation     `json:"operations"`
}

// JSON is the patch body to send to the API server.
func (p Patch) JSON() ([]byte, error) { return json.Marshal(p.Operations) }

// Rollout is the part of a live Rollout the patch is built against.
type Rollout struct {
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Spec struct {
		Strategy struct {
			Canary *struct {
				Steps json.RawMessage `json:"steps"`
			} `json:"canary"`
		} `json:"strategy"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

const (
	imagePathPrefix = "/spec/template/spec/containers/"
	stepsPath       = "/spec/strategy/canary/steps"
	versionPath     = "/metadata/resourceVersion"
)

// Build produces the patch for one release.
//
// The container is found by name, at whatever index it currently occupies. A
// stored index would be a stored assumption about the order of a list somebody
// else maintains, and the failure mode is releasing the application's image
// into its logging sidecar.
func Build(live []byte, container, candidateImage, lane string, weights []int) (Patch, error) {
	var rollout Rollout
	if err := json.Unmarshal(live, &rollout); err != nil {
		return Patch{}, release.Invalid("unreadable_rollout", "rollout",
			fmt.Sprintf("the Rollout did not decode: %v", err),
			"Check that the cluster returned a Rollout.")
	}

	index := -1
	for i, c := range rollout.Spec.Template.Spec.Containers {
		if c.Name == container {
			index = i
			break
		}
	}
	if index < 0 {
		return Patch{}, release.Invalid("container_not_found", "artifact.container",
			fmt.Sprintf("Rollout %q has no container called %q", rollout.Metadata.Name, container),
			"Register this application again to pick up the change.")
	}
	if strings.TrimSpace(candidateImage) == "" {
		return Patch{}, release.Invalid("missing_image", "artifact",
			"no candidate image was given",
			"Resolve the candidate to a digest first.")
	}

	steps, err := Steps(weights)
	if err != nil {
		return Patch{}, err
	}

	previous := rollout.Spec.Template.Spec.Containers[index].Image
	imagePath := fmt.Sprintf("%s%d/image", imagePathPrefix, index)

	patch := Patch{
		RolloutUID:      rollout.Metadata.UID,
		ResourceVersion: rollout.Metadata.ResourceVersion,
		Container:       container,
		ContainerIndex:  index,
		PreviousImage:   previous,
		CandidateImage:  candidateImage,
		Lane:            lane,
		Weights:         append([]int(nil), weights...),
	}
	if rollout.Spec.Strategy.Canary != nil {
		patch.PreviousSteps = rollout.Spec.Strategy.Canary.Steps
	}

	patch.Operations = []Operation{
		// The tests come first and are part of the same document, so the API
		// server rejects the whole thing if either moved. "SafeLane cannot
		// overwrite something it did not see" holds even when two SafeLanes
		// run at once.
		{Op: "test", Path: versionPath, Value: mustJSON(rollout.Metadata.ResourceVersion)},
		{Op: "test", Path: imagePath, Value: mustJSON(previous)},
		{Op: "replace", Path: imagePath, Value: mustJSON(candidateImage)},
		{Op: "replace", Path: stepsPath, Value: steps},
	}
	return patch, nil
}

// Steps turns a lane's weights into canary steps.
//
// Each weight but the last becomes a `setWeight` followed by an indefinite
// pause. The last weight is not a step at all: Argo reaches full traffic by
// running out of steps after the final promotion.
//
// Every pause is indefinite. A timed pause would resume on its own, and a
// rollout that widens because a clock ran out is a rollout nobody decided to
// widen - which is the one behaviour this whole design exists to prevent.
func Steps(weights []int) (json.RawMessage, error) {
	if len(weights) == 0 {
		return nil, release.Invalid("empty_lane", "lane",
			"the lane declares no weights",
			"Every lane needs at least one weight, ending at 100.")
	}
	if last := weights[len(weights)-1]; last != 100 {
		return nil, release.Invalid("lane_does_not_finish", "lane",
			fmt.Sprintf("the lane ends at %d%%", last),
			"End every lane at 100.")
	}

	steps := make([]map[string]any, 0, (len(weights)-1)*2)
	for _, weight := range weights[:len(weights)-1] {
		steps = append(steps,
			map[string]any{"setWeight": weight},
			// An empty pause is Argo's indefinite pause. `{}` and
			// `{"duration": ...}` differ by one key and by everything.
			map[string]any{"pause": map[string]any{}},
		)
	}
	return json.Marshal(steps)
}

// Verify checks that applying this patch changed exactly what it said it would.
//
// It compares the whole object before and after with the two patched paths
// removed. Anything else that moved shows up as a difference, including fields
// this package has never heard of - which is the only way to check a boundary
// against a schema that keeps growing.
func (p Patch) Verify(before, after []byte) error {
	strippedBefore, err := withoutPatchedPaths(before, p.ContainerIndex)
	if err != nil {
		return err
	}
	strippedAfter, err := withoutPatchedPaths(after, p.ContainerIndex)
	if err != nil {
		return err
	}

	if diff := firstDifference(strippedBefore, strippedAfter, ""); diff != "" {
		return release.Invalid("patch_changed_more_than_it_said", "rollout",
			fmt.Sprintf("applying this release also changed %s", diff),
			"Nothing outside the container image and the canary steps may change.")
	}
	return nil
}

// withoutPatchedPaths removes exactly the two paths the patch is allowed to
// touch, plus the fields Kubernetes moves on any write.
func withoutPatchedPaths(raw []byte, containerIndex int) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, release.Invalid("unreadable_rollout", "rollout",
			fmt.Sprintf("the Rollout did not decode: %v", err),
			"Check that the cluster returned a Rollout.")
	}

	// resourceVersion, generation and managedFields change on every write and
	// are not SafeLane's doing. status is Argo's, and Argo is meant to move it.
	if metadata, ok := object["metadata"].(map[string]any); ok {
		delete(metadata, "resourceVersion")
		delete(metadata, "generation")
		delete(metadata, "managedFields")
		delete(metadata, "annotations")
	}
	delete(object, "status")

	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		return object, nil
	}
	if strategy, ok := spec["strategy"].(map[string]any); ok {
		if canary, ok := strategy["canary"].(map[string]any); ok {
			delete(canary, "steps")
		}
	}
	if template, ok := spec["template"].(map[string]any); ok {
		if podSpec, ok := template["spec"].(map[string]any); ok {
			if containers, ok := podSpec["containers"].([]any); ok && containerIndex < len(containers) {
				if container, ok := containers[containerIndex].(map[string]any); ok {
					delete(container, "image")
				}
			}
		}
	}
	return object, nil
}

// firstDifference walks two decoded objects and names the first path where
// they differ, so the failure says which field moved rather than that
// something did.
func firstDifference(before, after any, path string) string {
	switch left := before.(type) {
	case map[string]any:
		right, ok := after.(map[string]any)
		if !ok {
			return path
		}
		for key, value := range left {
			other, present := right[key]
			if !present {
				return path + "/" + key
			}
			if diff := firstDifference(value, other, path+"/"+key); diff != "" {
				return diff
			}
		}
		for key := range right {
			if _, present := left[key]; !present {
				return path + "/" + key
			}
		}
		return ""
	case []any:
		right, ok := after.([]any)
		if !ok || len(left) != len(right) {
			return path
		}
		for i := range left {
			if diff := firstDifference(left[i], right[i], fmt.Sprintf("%s/%d", path, i)); diff != "" {
				return diff
			}
		}
		return ""
	default:
		if fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after) {
			return path
		}
		return ""
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}
