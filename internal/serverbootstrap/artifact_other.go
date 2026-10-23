//go:build !unix

package serverbootstrap

import "os"

func openArtifact(ArtifactSource, Artifact) (*os.File, string, error) {
	return nil, "", InvalidArtifact
}
