package rest

import (
	"testing"

	"github.com/mimalef70/goomni/src/internal/capacitycheck"
)

func TestCapacityResourcePreflight(t *testing.T) { capacitycheck.Run(t) }
