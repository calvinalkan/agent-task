package cli_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/calvinalkan/agent-task/internal/testutil"
)

func Test_CLI_Matches_Model_When_Curated_Seed_Applied(t *testing.T) {
	t.Parallel()

	maxOps := 150
	if testing.Short() {
		maxOps = 50
	}

	for _, seed := range testutil.CuratedSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			cfg := testutil.DefaultRunConfig()
			cfg.MaxOps = maxOps
			cfg.CompareStateEveryN = 10

			testutil.RunBehaviorWithSeed(t, seed.Data, cfg)
		})
	}
}

func Test_CLI_Matches_Model_When_Seeded_Random_Ops_Applied(t *testing.T) {
	t.Parallel()

	seedsCount := 10
	if testing.Short() {
		seedsCount = 3
	}

	bytesPerSeed := 4096

	for seedIndex := range seedsCount {
		seed := uint64(seedIndex + 1)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewPCG(seed, seed))
			fuzzBytes := make([]byte, bytesPerSeed)
			fillRandom(rng, fuzzBytes)

			cfg := testutil.DefaultRunConfig()
			cfg.MaxOps = 120
			cfg.CompareStateEveryN = 10

			testutil.RunBehaviorWithSeed(t, fuzzBytes, cfg)
		})
	}
}

func fillRandom(rng *rand.Rand, dst []byte) {
	for i := range dst {
		dst[i] = byte(rng.Uint32())
	}
}
