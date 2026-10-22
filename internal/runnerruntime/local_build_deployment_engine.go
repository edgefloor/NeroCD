package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
)

func hostingReleaseImage(release domain.HostingRelease) string {
	if release.LocalBuild != (domain.HostingBuildArtifact{}) {
		return release.ImageID
	}
	return release.CanonicalImage
}

func (e *imageEngine) prepareLocalBuild(ctx context.Context, snapshot domain.HostingSnapshot) (domain.HostingRelease, error) {
	release, err := domain.NewHostingLocalBuildRelease(snapshot.LocalBuild.Artifact)
	if err != nil || domain.ValidateHostingSnapshotRelease(snapshot, release) != nil {
		return domain.HostingRelease{}, errors.New("invalid admitted local artifact")
	}
	if err = e.verifyLocalRelease(ctx, release); err != nil {
		return domain.HostingRelease{}, err
	}
	if err = e.verifyDaemon(ctx, release.DaemonID); err != nil {
		return domain.HostingRelease{}, err
	}
	return release, nil
}
