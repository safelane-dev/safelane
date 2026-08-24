package oci

import "testing"

func TestReadLabelsRejectsMixedMetadataInEitherOrder(t *testing.T) {
	labelled := PlatformLabels{
		Platform: "linux/amd64",
		Labels: map[string]string{
			labelSource:   "https://github.com/acme/payments-api",
			labelRevision: "1111111111111111111111111111111111111111",
		},
	}
	unlabelled := PlatformLabels{Platform: "linux/arm64", Labels: map[string]string{}}

	for _, platforms := range [][]PlatformLabels{
		{labelled, unlabelled},
		{unlabelled, labelled},
	} {
		if _, err := readLabels(platforms); err == nil {
			t.Fatal("mixed platform metadata was accepted")
		}
	}
}
