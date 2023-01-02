package queued_world

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"sync/atomic"
)

type Txn struct {
	complete atomic.Bool
	w        *World
}

func (txn *Txn) SetBlock(pos cube.Pos, b world.Block, opts *world.SetOpts) {
	txn.world().setBlock(pos, b, opts)
}

func (txn *Txn) Block(pos cube.Pos) world.Block {
	return txn.world().block(pos)
}

func (txn *Txn) SetBiome(pos cube.Pos, b world.Biome) {
	txn.world().setBiome(pos, b)
}

func (txn *Txn) Biome(pos cube.Pos) world.Biome {
	return txn.world().biome(pos)
}

// world returns the world of the Txn. It panics if the transaction was already
// marked complete.
func (txn *Txn) world() *World {
	if txn.complete.Load() {
		panic("transaction already complete")
	}
	return txn.w
}
