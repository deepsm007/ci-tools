package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/ci-tools/pkg/api"
)

func TestGetJobInfo(t *testing.T) {
	testCases := []struct {
		name      string
		expected  jobInfo
		isRelease bool
	}{{
		name: "release-openshift-origin-installer-e2e-gcp-upgrade-4.7",
		expected: jobInfo{
			As:      "e2e-gcp-upgrade",
			Product: "origin",
			Version: "4.7",
		},
		isRelease: true,
	}, {
		name:      "promote-release-openshift-machine-os-content-e2e-aws-4.7",
		isRelease: false,
	}}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			info, isRelease := getJobInfo(testCase.name)
			if isRelease != testCase.isRelease {
				t.Errorf("wrong `isNotRelease`. Actual: %t, Expected: %t", isRelease, testCase.isRelease)
			}
			if info.As != testCase.expected.As {
				t.Errorf("wrong `as`. Actual: %s, Expected: %s", info.As, testCase.expected.As)
			}
			if info.Product != testCase.expected.Product {
				t.Errorf("wrong `product`. Actual: %s, Expected: %s", info.Product, testCase.expected.Product)
			}
			if info.Version != testCase.expected.Version {
				t.Errorf("wrong `version`. Actual: %s, Expected: %s", info.Version, testCase.expected.Version)
			}
		})
	}
}

func TestUpdateBaseImages(t *testing.T) {
	testCases := []struct {
		name              string
		newImages         map[string]api.ImageStreamTagReference
		ciopImages        map[string]api.ImageStreamTagReference
		replacementImages map[string]api.ImageStreamTagReference
		expectedImages    map[string]api.ImageStreamTagReference
		version           string
		expectedErr       bool
	}{{
		name: "No conflict",
		newImages: map[string]api.ImageStreamTagReference{
			"new-image": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "new-image",
			},
		},
		ciopImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		replacementImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		expectedImages: map[string]api.ImageStreamTagReference{
			"new-image": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "new-image",
			},
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		version:     "4.7",
		expectedErr: false,
	}, {
		name: "ciop-conflict",
		newImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.6",
				Tag:       "base",
			},
		},
		ciopImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		replacementImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		expectedImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		version:     "4.7",
		expectedErr: true,
	}, {
		name: "replacements-conflict",
		newImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "new-dev-scripts",
			},
		},
		ciopImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		replacementImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		expectedImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		version:     "4.7",
		expectedErr: true,
	}, {
		name: "non-conlict overlap",
		newImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		ciopImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		replacementImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		expectedImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		version:     "4.7",
		expectedErr: false,
	}, {
		name: "empty config gets filled in",
		newImages: map[string]api.ImageStreamTagReference{
			"myimage": {
				Tag: "myimage",
			},
		},
		ciopImages: map[string]api.ImageStreamTagReference{
			"base": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "base",
			},
		},
		replacementImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
		},
		expectedImages: map[string]api.ImageStreamTagReference{
			"dev-scripts": {
				Namespace: "openshift-kni",
				Name:      "test",
				Tag:       "dev-scripts",
			},
			"myimage": {
				Namespace: "ocp",
				Name:      "4.7",
				Tag:       "myimage",
			},
		},
		version:     "4.7",
		expectedErr: false,
	}}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := updateBaseImages(testCase.newImages, testCase.ciopImages, testCase.replacementImages, testCase.version); err != nil && !testCase.expectedErr {
				t.Errorf("received error when one was not expected: %v", err)
			} else if err == nil && testCase.expectedErr {
				t.Error("Did not get error when one was expected")
			} else {
				if diff := cmp.Diff(testCase.replacementImages, testCase.expectedImages); diff != "" {
					t.Errorf("expected does not match actual: %s", diff)
				}
			}
		})
	}
}
