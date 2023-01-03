package world

import (
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// Generator handles the generating of newly created chunks. Worlds have one generator which is used to
// generate chunks when the provider of the World cannot find a chunk at a given chunk position.
type Generator interface {
	// GenerateChunk generates a chunk at a chunk position passed. The generator sets blocks in the chunk that
	// is passed to the method.
	GenerateChunk(pos ChunkPos, chunk *chunk.Chunk)
}

// NopGenerator is the default generator a World. It places no blocks in the World which results in a void
// World.
type NopGenerator struct{}

// GenerateChunk ...
func (NopGenerator) GenerateChunk(ChunkPos, *chunk.Chunk) {}
