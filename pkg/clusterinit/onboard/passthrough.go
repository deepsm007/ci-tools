package onboard

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"

	"github.com/sirupsen/logrus"

	"github.com/openshift/ci-tools/pkg/clusterinit/clusterinstall"
	cinitmanifest "github.com/openshift/ci-tools/pkg/clusterinit/manifest"
	cinittypes "github.com/openshift/ci-tools/pkg/clusterinit/types"
)

const (
	passthroughRoot string = "manifests"
)

var (
	tripleHyphen = regexp.MustCompile(`^\-\-\-$`)
	//go:embed manifests
	manifests embed.FS
)

type passthroughGenerator struct {
	clusterInstall *clusterinstall.ClusterInstall
	manifests      fs.FS
	readFile       func(fsys fs.FS, name string) ([]byte, error)
}

func (s *passthroughGenerator) Name() string {
	return "passthrough-manifests"
}

func (s *passthroughGenerator) Skip() cinittypes.SkipStep {
	return s.clusterInstall.Onboard.PassthroughManifest.SkipStep
}

func (s *passthroughGenerator) ExcludedManifests() cinittypes.ExcludeManifest {
	return s.clusterInstall.Onboard.PassthroughManifest.ExcludeManifest
}

func (s *passthroughGenerator) Patches() []cinitmanifest.Patch {
	return s.clusterInstall.Onboard.PassthroughManifest.Patches
}

func (s *passthroughGenerator) Generate(ctx context.Context, log *logrus.Entry) (map[string][]interface{}, error) {
	clusterRoot := BuildFarmDirFor(s.clusterInstall.Onboard.ReleaseRepo, s.clusterInstall.ClusterName)

	subFS, err := fs.Sub(s.manifests, passthroughRoot)
	if err != nil {
		return nil, fmt.Errorf("subfs: %w", err)
	}

	pathToManifests := make(map[string][]interface{})

	if err := fs.WalkDir(subFS, ".", func(p string, d fs.DirEntry, _ error) error {
		if p == "." || d.IsDir() {
			return nil
		}

		bytes, err := s.readFile(subFS, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}

		splitStrings := tripleHyphen.Split(string(bytes), -1)
		data := make([]interface{}, len(splitStrings))
		for i := range splitStrings {
			data[i] = []byte(splitStrings[i])
		}

		pathToManifests[path.Join(clusterRoot, p)] = data

		return nil
	}); err != nil {
		return nil, fmt.Errorf("create manifests: %w", err)
	}

	return pathToManifests, nil
}

func NewPassthroughGenerator(log *logrus.Entry, clusterInstall *clusterinstall.ClusterInstall) *passthroughGenerator {
	return &passthroughGenerator{
		clusterInstall: clusterInstall,
		manifests:      manifests,
		readFile:       fs.ReadFile,
	}
}
