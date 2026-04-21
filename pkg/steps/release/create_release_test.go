package release

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	imagev1 "github.com/openshift/api/image/v1"

	"github.com/openshift/ci-tools/pkg/api"
)

func TestOcAdmReleaseNewArgs(t *testing.T) {
	sourceTagReference := imagev1.SourceTagReferencePolicy

	var testCases = []struct {
		name        string
		config      *api.ReleaseTagConfiguration
		namespace   string
		streamName  string
		cvo         string
		destination string
		version     string
		want        string
	}{
		{
			name:        "4.10 no keep-manifest-list",
			config:      &api.ReleaseTagConfiguration{Name: "4.10"},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver",
		},
		{
			name:        "4.11 with keep-manifest-list",
			config:      &api.ReleaseTagConfiguration{Name: "4.11"},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver --keep-manifest-list",
		},
		{
			name:        "4.12 with keep-manifest-list and reference mode",
			config:      &api.ReleaseTagConfiguration{Name: "4.12", ReferencePolicy: &sourceTagReference},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver --keep-manifest-list",
		},
		{
			name:        "4.15 with keep-manifest-list and reference mode",
			config:      &api.ReleaseTagConfiguration{Name: "4.15", ReferencePolicy: &sourceTagReference},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver --reference-mode=source --keep-manifest-list",
		},
		{
			name:        "malformed version returns no keep-manifest-list",
			config:      &api.ReleaseTagConfiguration{Name: "not-a-version"},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver",
		},
		{
			name:        "malformed version with reference policy yields no extra flags",
			config:      &api.ReleaseTagConfiguration{Name: "oops", ReferencePolicy: &sourceTagReference},
			namespace:   "ns",
			streamName:  "stream",
			cvo:         "cvo",
			destination: "dest",
			version:     "ver",
			want:        "oc adm release new --max-per-registry=32 -n ns --from-image-stream stream --to-image-base cvo --to-image dest --name ver",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := strings.Join(ocAdmReleaseNewArgs(
				testCase.config,
				testCase.namespace,
				testCase.streamName,
				testCase.cvo,
				testCase.destination,
				testCase.version,
				"",
			), " ")
			if diff := cmp.Diff(testCase.want, got); diff != "" {
				t.Errorf("%s: unexpected argv: %s", testCase.name, diff)
			}
		})
	}
}

func TestBuildOcAdmReleaseNewCommand(t *testing.T) {
	cfg := &api.ReleaseTagConfiguration{Name: "4.15"}
	got := buildOcAdmReleaseNewCommand(cfg, "ns", "stable", "cvo", "dest:tag", "ver")
	var testCases = []struct {
		name string
		want string
	}{
		{"exports imagestream to file", `oc get imagestream -n "ns" "stable"`},
		{"captures oc get stderr", "GET_ERR"},
		{"logs stderr on get failure", "failed, stderr:"},
		{"uses from-image-stream-file", "--from-image-stream-file=${RELEASE_IS_FILE}"},
		{"file then stream with or", " || "},
		{"from-image-stream fallback", "--from-image-stream stable"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if !strings.Contains(got, testCase.want) {
				t.Errorf("%s: got does not contain %q:\n%s", testCase.name, testCase.want, got)
			}
		})
	}
}
