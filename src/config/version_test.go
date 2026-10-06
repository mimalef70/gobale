package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseStageDistinguishesArtifactVersionFromProtocolEvidence(t *testing.T) {
	for version, stage := range map[string]string{
		"1.0.0": "release", "0.2.0-alpha.1": "alpha", "1.1.0-beta.2": "beta",
		"1.1.0-rc.1": "rc", "dev": "development", "01.0.0": "development",
	} {
		require.Equal(t, stage, ReleaseStage(version), version)
	}
}
