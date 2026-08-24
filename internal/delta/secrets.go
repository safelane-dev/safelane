package delta

import (
	"encoding/json"
	"sort"
	"strings"
)

// Workload is the parts of a Rollout's pod template the evidence boundary
// cares about. It is decoded from the Rollout SafeLane read, and the only
// thing that ever crosses out of it is names.
type Workload struct {
	Spec struct {
		Replicas *int `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
					Env  []struct {
						Name string `json:"name"`
						// Value is decoded so it can be dropped. A field that
						// is never decoded is a field somebody adds back later
						// without noticing; a field that is decoded and
						// explicitly discarded is a decision with a comment on
						// it.
						Value     string `json:"value"`
						ValueFrom *struct {
							SecretKeyRef *struct {
								Name string `json:"name"`
								Key  string `json:"key"`
							} `json:"secretKeyRef"`
							ConfigMapKeyRef *struct {
								Name string `json:"name"`
								Key  string `json:"key"`
							} `json:"configMapKeyRef"`
						} `json:"valueFrom"`
					} `json:"env"`
					EnvFrom []struct {
						SecretRef *struct {
							Name string `json:"name"`
						} `json:"secretRef"`
						ConfigMapRef *struct {
							Name string `json:"name"`
						} `json:"configMapRef"`
					} `json:"envFrom"`
				} `json:"containers"`
				Volumes []struct {
					Name   string `json:"name"`
					Secret *struct {
						SecretName string `json:"secretName"`
					} `json:"secret"`
					ConfigMap *struct {
						Name string `json:"name"`
					} `json:"configMap"`
				} `json:"volumes"`
				ImagePullSecrets []struct {
					Name string `json:"name"`
				} `json:"imagePullSecrets"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// SecretReferencesIn reads a Rollout and returns only the names of the Secrets
// and ConfigMaps it uses.
//
// Nothing else comes back. Not one environment variable's value, not one
// mounted key's contents, not one registry credential. A name answers "what
// does this workload depend on", which is the deployment question; a value
// answers a question nobody asked and would then live in the assessment, the
// stored proof, and the terminal.
func SecretReferencesIn(rollout []byte) []string {
	var workload Workload
	if err := json.Unmarshal(rollout, &workload); err != nil {
		return nil
	}

	seen := map[string]bool{}
	add := func(kind, name string) {
		if name == "" {
			return
		}
		seen[kind+"/"+name] = true
	}
	spec := workload.Spec.Template.Spec
	for _, container := range spec.Containers {
		for _, env := range container.Env {
			// env.Value is deliberately not read. Decoding it and dropping it
			// is the point: the field exists, and it stops here.
			if env.ValueFrom == nil {
				continue
			}
			if ref := env.ValueFrom.SecretKeyRef; ref != nil {
				add("Secret", ref.Name)
			}
			if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
				add("ConfigMap", ref.Name)
			}
		}
		for _, from := range container.EnvFrom {
			if from.SecretRef != nil {
				add("Secret", from.SecretRef.Name)
			}
			if from.ConfigMapRef != nil {
				add("ConfigMap", from.ConfigMapRef.Name)
			}
		}
	}
	for _, volume := range spec.Volumes {
		if volume.Secret != nil {
			add("Secret", volume.Secret.SecretName)
		}
		if volume.ConfigMap != nil {
			add("ConfigMap", volume.ConfigMap.Name)
		}
	}
	for _, pull := range spec.ImagePullSecrets {
		add("Secret", pull.Name)
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ReplicasIn reads the Rollout's replica count, for describing exposure when
// there is no traffic router.
func ReplicasIn(rollout []byte) int {
	var workload Workload
	if err := json.Unmarshal(rollout, &workload); err != nil {
		return 0
	}
	if workload.Spec.Replicas == nil {
		return 0
	}
	return *workload.Spec.Replicas
}

// excludeSecrets is the capture-time guard on deployment evidence.
//
// It normalises the reference names and makes sure nothing that looks like a
// value survived whatever built the input. This is belt and braces on purpose:
// the boundary is the one place worth being paranoid, because everything
// downstream trusts that it held.
func excludeSecrets(evidence DeploymentEvidence) DeploymentEvidence {
	out := evidence
	names := make([]string, 0, len(evidence.SecretReferences))
	seen := map[string]bool{}
	for _, reference := range evidence.SecretReferences {
		// A reference is `Kind/name`. Anything carrying an `=` or more than
		// one separator is not a reference, it is content, and content does
		// not belong here.
		if reference == "" || strings.ContainsAny(reference, "=\n") {
			continue
		}
		if seen[reference] {
			continue
		}
		seen[reference] = true
		names = append(names, reference)
	}
	sort.Strings(names)
	out.SecretReferences = names
	return out
}

// reduceSecretHunks replaces a changed file that touches a known secret
// reference with the path and the reference name.
//
// The rule is narrow on purpose. It is not a scanner and not a guess about
// what looks secret: it reduces a hunk when the change touches something this
// workload actually mounts or reads. Anything broader would quietly hide
// ordinary changes and teach people to distrust the evidence.
func reduceSecretHunks(files []File, references []string) []File {
	if len(references) == 0 || len(files) == 0 {
		return files
	}
	out := make([]File, 0, len(files))
	for _, file := range files {
		file.SecretReference = SecretReferenceForPath(string(file.Path), references)
		out = append(out, file)
	}
	return out
}

// SecretReferenceForPath reports the workload reference whose name appears in
// a changed path. It is exported so the raw-diff boundary can exclude the same
// file before content-addressing or retrieval.
func SecretReferenceForPath(path string, references []string) string {
	lowerPath := strings.ToLower(path)
	for _, reference := range references {
		name := reference
		if _, after, found := strings.Cut(reference, "/"); found {
			name = after
		}
		if name != "" && strings.Contains(lowerPath, strings.ToLower(name)) {
			return reference
		}
	}
	return ""
}
