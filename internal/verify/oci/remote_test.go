package oci_test

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

// These tests run against a real OCI Distribution registry, in this process,
// over HTTP. Nothing here is GHCR-shaped: if the package works against this it
// works against any registry, which is the whole claim of decision 5.
func testRegistry(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func resolver(host string) oci.Resolver {
	return oci.Resolver{Registry: oci.Remote{Keychain: authn.DefaultKeychain, Insecure: true}}
}

// pushImage publishes a single-platform image with the given labels.
func pushImage(t *testing.T, repository, tag string, labels map[string]string) v1.Hash {
	t.Helper()
	return pushImageAs(t, repository, tag, labels, nil)
}

func pushImageAs(t *testing.T, repository, tag string, labels map[string]string, keychain authn.Keychain) v1.Hash {
	t.Helper()
	image, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	image = withLabels(t, image, labels, "linux", "amd64")

	ref, err := name.NewTag(repository+":"+tag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	var opts []remote.Option
	if keychain != nil {
		opts = append(opts, remote.WithAuthFromKeychain(keychain))
	}
	if err := remote.Write(ref, image, opts...); err != nil {
		t.Fatal(err)
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// pushIndex publishes a multi-platform index, one entry per set of labels.
func pushIndex(t *testing.T, repository, tag string, perPlatform map[string]map[string]string) v1.Hash {
	t.Helper()
	index := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for platform, labels := range perPlatform {
		os, arch, _ := strings.Cut(platform, "/")
		image, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		image = withLabels(t, image, labels, os, arch)
		index = mutate.AppendManifests(index, mutate.IndexAddendum{
			Add:        image,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: os, Architecture: arch}},
		})
	}

	ref, err := name.NewTag(repository+":"+tag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, index); err != nil {
		t.Fatal(err)
	}
	digest, err := index.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func withLabels(t *testing.T, image v1.Image, labels map[string]string, os, arch string) v1.Image {
	t.Helper()
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config = config.DeepCopy()
	config.OS, config.Architecture = os, arch
	config.Config.Labels = labels
	updated, err := mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func provenance(revision string) map[string]string {
	return map[string]string{
		"org.opencontainers.image.source":   "https://github.com/acme/payments-api",
		"org.opencontainers.image.revision": revision,
	}
}

const (
	revisionA = "1111111111111111111111111111111111111111"
	revisionB = "2222222222222222222222222222222222222222"
)

// A tag resolves to a digest before anything else happens. Everything
// downstream describes that digest, and a tag that moved in between would make
// all of it describe something no longer true.
func TestResolveTurnsATagIntoADigest(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	want := pushImage(t, repository, "v1", provenance(revisionA))

	artifact, err := resolver(host).Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if artifact.Digest != want.String() {
		t.Errorf("digest = %s, want %s", artifact.Digest, want)
	}
	if artifact.Reference() != repository+"@"+want.String() {
		t.Errorf("reference = %s", artifact.Reference())
	}
}

func TestResolveAcceptsADigestUnchanged(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	digest := pushImage(t, repository, "v1", provenance(revisionA))

	artifact, err := resolver(host).Resolve(context.Background(), repository, digest.String())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if artifact.Digest != digest.String() {
		t.Errorf("digest = %s", artifact.Digest)
	}
}

func TestReadSourceReadsTheImageLabels(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", provenance(revisionA))

	r := resolver(host)
	artifact, err := r.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := r.ReadSource(context.Background(), artifact)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if metadata.Revision != revisionA {
		t.Errorf("revision = %s", metadata.Revision)
	}
	if metadata.Method != oci.BindingOCILabels {
		t.Errorf("method = %s, want oci_labels", metadata.Method)
	}
}

// Every runnable platform is inspected, and they have to agree.
func TestConsistentMultiPlatformIndexIsAccepted(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushIndex(t, repository, "v1", map[string]map[string]string{
		"linux/amd64": provenance(revisionA),
		"linux/arm64": provenance(revisionA),
	})

	r := resolver(host)
	artifact, err := r.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := r.ReadSource(context.Background(), artifact)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if metadata.Revision != revisionA {
		t.Errorf("revision = %s", metadata.Revision)
	}
}

// An index whose arm64 image says one commit and whose amd64 image says
// another is not a build of one revision. It is two builds sharing a tag, and
// which one a cluster runs depends on which node the pod lands on.
func TestInconsistentMultiPlatformIndexIsRefused(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushIndex(t, repository, "v1", map[string]map[string]string{
		"linux/amd64": provenance(revisionA),
		"linux/arm64": provenance(revisionB),
	})

	r := resolver(host)
	artifact, err := r.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ReadSource(context.Background(), artifact)
	assertRejection(t, err, "inconsistent_source_metadata")
}

func TestAPlatformWithNoLabelsMakesTheIndexInconsistent(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushIndex(t, repository, "v1", map[string]map[string]string{
		"linux/amd64": provenance(revisionA),
		"linux/arm64": {},
	})

	r := resolver(host)
	artifact, err := r.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ReadSource(context.Background(), artifact)
	assertRejection(t, err, "inconsistent_source_metadata")
}

func TestAnImageWithNoProvenanceIsAnHonestFailure(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", nil)

	r := resolver(host)
	artifact, err := r.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.ReadSource(context.Background(), artifact)
	assertRejection(t, err, "unbound_artifact")
}

// FindRevision matches on the proved revision, never on how a tag is spelled.
func TestFindRevisionMatchesOnProvedProvenance(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	want := pushImage(t, repository, "release-7", provenance(revisionB))
	// A tag that spells one revision while carrying another. The spelling is a
	// string somebody chose; the labels are the fact.
	pushImage(t, repository, "sha-"+revisionB, provenance(revisionA))

	artifact, err := resolver(host).FindRevision(context.Background(), repository, "", revisionB)
	if err != nil {
		t.Fatalf("FindRevision: %v", err)
	}
	if artifact.Digest != want.String() {
		t.Errorf("FindRevision followed the tag's spelling instead of its provenance: got %s, want %s",
			artifact.Digest, want)
	}
}

// Nothing published for that revision is a failure that asks for an exact
// reference. Deploying an older container than the one asked for is the
// specific accident this package exists to prevent.
func TestFindRevisionRefusesToSettleForAnOlderImage(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", provenance(revisionA))
	pushImage(t, repository, "latest", provenance(revisionA))

	_, err := resolver(host).FindRevision(context.Background(), repository, "", revisionB)
	assertRejection(t, err, "revision_not_published")
	assertRemedy(t, err, "exact image reference")
}

func TestFindRevisionSkipsAnotherProjectsImages(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", map[string]string{
		"org.opencontainers.image.source":   "https://github.com/acme/orders-api",
		"org.opencontainers.image.revision": revisionA,
	})

	_, err := resolver(host).FindRevision(context.Background(), repository,
		"https://github.com/acme/payments-api", revisionA)
	assertRejection(t, err, "revision_not_published")
}

// The same repository written with a trailing .git is the same repository.
func TestSourceComparisonIgnoresRepositorySpelling(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", map[string]string{
		"org.opencontainers.image.source":   "https://github.com/acme/payments-api.git",
		"org.opencontainers.image.revision": revisionA,
	})

	if _, err := resolver(host).FindRevision(context.Background(), repository,
		"https://github.com/acme/payments-api", revisionA); err != nil {
		t.Errorf("FindRevision: %v", err)
	}
}

// privateRegistry refuses anything without HTTP Basic credentials, so the
// credential path is proved rather than merely wired.
func privateRegistry(t *testing.T, username, password string) string {
	t.Helper()
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
	server := httptest.NewServer(guarded)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

// A private registry is reached with the credentials the user already has.
// SafeLane keeps none of its own: there is nothing here to leak, rotate, or
// scope wrongly.
func TestPrivateRegistryUsesExistingDockerCredentials(t *testing.T) {
	host := privateRegistry(t, "someone", "a-token")
	repository := host + "/acme/payments-api"
	keychain := staticKeychain{username: "someone", password: "a-token"}

	pushImageAs(t, repository, "v1", provenance(revisionA), keychain)

	authenticated := oci.Resolver{Registry: oci.Remote{Insecure: true, Keychain: keychain}}
	artifact, err := authenticated.Resolve(context.Background(), repository, "v1")
	if err != nil {
		t.Fatalf("Resolve with credentials: %v", err)
	}
	if _, err := authenticated.ReadSource(context.Background(), artifact); err != nil {
		t.Fatalf("ReadSource with credentials: %v", err)
	}

	// The same read with no credentials is unknown evidence, not a silent
	// "there is no such image".
	anonymous := oci.Resolver{Registry: oci.Remote{Insecure: true, Keychain: authn.DefaultKeychain}}
	_, err = anonymous.Resolve(context.Background(), repository, "v1")
	assertRejection(t, err, "registry_unreachable")
}

// An anonymous registry needs no setup at all.
func TestAnonymousRegistryNeedsNoCredentials(t *testing.T) {
	host := testRegistry(t)
	repository := host + "/acme/payments-api"
	pushImage(t, repository, "v1", provenance(revisionA))

	anonymous := oci.Resolver{Registry: oci.Remote{Insecure: true, Keychain: authn.DefaultKeychain}}
	if _, err := anonymous.Resolve(context.Background(), repository, "v1"); err != nil {
		t.Fatalf("Resolve anonymously: %v", err)
	}
}

type staticKeychain struct{ username, password string }

func (k staticKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.FromConfig(authn.AuthConfig{Username: k.username, Password: k.password}), nil
}

func TestAnUnreachableRegistryIsUnknownNotFailed(t *testing.T) {
	_, err := resolver("").Resolve(context.Background(),
		"127.0.0.1:1/acme/payments-api", "v1")
	assertRejection(t, err, "registry_unreachable")
}
