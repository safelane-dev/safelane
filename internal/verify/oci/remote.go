package oci

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Remote is the production [Registry]: any OCI Distribution registry, reached
// anonymously or with whatever Docker credentials the user already has.
//
// Credentials come from the standard Docker keychain - `~/.docker/config.json`
// and its credential helpers - so a user who can `docker pull` an image can
// release it, and a public image needs no setup at all. SafeLane stores no
// registry credentials of its own, which is the point: there is nothing here
// to leak, rotate, or scope wrongly.
type Remote struct {
	// Keychain overrides credential discovery. Nil means the Docker keychain.
	Keychain authn.Keychain
	// Insecure allows plain HTTP, for an in-process test registry.
	Insecure bool
}

func (r Remote) options(ctx context.Context) []remote.Option {
	keychain := r.Keychain
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(keychain)}
}

func (r Remote) repository(repository string) (name.Repository, error) {
	var opts []name.Option
	if r.Insecure {
		opts = append(opts, name.Insecure)
	}
	return name.NewRepository(repository, opts...)
}

// Resolve turns a tag or digest into a manifest digest.
func (r Remote) Resolve(ctx context.Context, repository, reference string) (string, error) {
	repo, err := r.repository(repository)
	if err != nil {
		return "", err
	}
	ref, err := parseReference(repo, reference)
	if err != nil {
		return "", err
	}
	descriptor, err := remote.Head(ref, r.options(ctx)...)
	if err != nil {
		return "", err
	}
	return descriptor.Digest.String(), nil
}

// Platforms returns the config labels of every runnable platform under a
// digest.
//
// Attestation manifests are left out. A registry that supports build
// attestations stores them as extra manifests in the same index, on the
// synthetic `unknown/unknown` platform; treating one as a platform whose
// labels must agree would make every attested image look inconsistent with
// itself.
func (r Remote) Platforms(ctx context.Context, repository, digest string) ([]PlatformLabels, error) {
	repo, err := r.repository(repository)
	if err != nil {
		return nil, err
	}
	ref := repo.Digest(digest)
	descriptor, err := remote.Get(ref, r.options(ctx)...)
	if err != nil {
		return nil, err
	}

	if !descriptor.MediaType.IsIndex() {
		image, imgErr := descriptor.Image()
		if imgErr != nil {
			return nil, imgErr
		}
		labels, platform, labelErr := labelsOf(image)
		if labelErr != nil {
			return nil, labelErr
		}
		return []PlatformLabels{{Platform: platform, Labels: labels}}, nil
	}

	index, err := descriptor.ImageIndex()
	if err != nil {
		return nil, err
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, err
	}

	var platforms []PlatformLabels
	for _, entry := range manifest.Manifests {
		if !runnable(entry) {
			continue
		}
		image, imgErr := index.Image(entry.Digest)
		if imgErr != nil {
			return nil, imgErr
		}
		labels, platform, labelErr := labelsOf(image)
		if labelErr != nil {
			return nil, labelErr
		}
		if entry.Platform != nil {
			platform = entry.Platform.OS + "/" + entry.Platform.Architecture
		}
		platforms = append(platforms, PlatformLabels{Platform: platform, Labels: labels})
	}
	return platforms, nil
}

// Tags lists a repository's tags.
func (r Remote) Tags(ctx context.Context, repository string) ([]string, error) {
	repo, err := r.repository(repository)
	if err != nil {
		return nil, err
	}
	return remote.List(repo, r.options(ctx)...)
}

// runnable reports whether an index entry is an image a node could run, as
// opposed to an attestation or a signature riding along in the same index.
func runnable(entry v1.Descriptor) bool {
	if entry.Platform != nil && entry.Platform.OS == "unknown" && entry.Platform.Architecture == "unknown" {
		return false
	}
	if entry.Annotations["vnd.docker.reference.type"] != "" {
		return false
	}
	switch entry.MediaType {
	case types.OCIManifestSchema1, types.DockerManifestSchema2, types.DockerManifestSchema1,
		types.DockerManifestSchema1Signed, types.OCIImageIndex, types.DockerManifestList:
		return true
	default:
		return false
	}
}

func labelsOf(image v1.Image) (map[string]string, string, error) {
	config, err := image.ConfigFile()
	if err != nil {
		return nil, "", err
	}
	platform := config.OS + "/" + config.Architecture
	if config.Config.Labels == nil {
		return map[string]string{}, platform, nil
	}
	return config.Config.Labels, platform, nil
}

func parseReference(repo name.Repository, reference string) (name.Reference, error) {
	if validDigest(reference) {
		return repo.Digest(reference), nil
	}
	if reference == "" {
		return nil, fmt.Errorf("no tag or digest given for %s", repo.Name())
	}
	return repo.Tag(reference), nil
}
