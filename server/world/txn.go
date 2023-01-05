package world

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/go-gl/mathgl/mgl64"
	"sync/atomic"
	"time"
)

type Txn struct {
	complete atomic.Bool
	w        *World
}

// Range returns the lower and upper bounds of the World that the Txn is
// operating on.
func (txn *Txn) Range() cube.Range {
	return txn.w.ra
}

func (txn *Txn) SetBlock(pos cube.Pos, b Block, opts *SetOpts) {
	txn.World().setBlock(pos, b, opts)
}

func (txn *Txn) Block(pos cube.Pos) Block {
	return txn.World().block(pos)
}

func (txn *Txn) Liquid(pos cube.Pos) (Liquid, bool) {
	return txn.World().liquid(pos)
}

func (txn *Txn) SetLiquid(pos cube.Pos, b Liquid) {
	txn.World().setLiquid(pos, b)
}

func (txn *Txn) BuildStructure(pos cube.Pos, s Structure) {
	txn.World().buildStructure(pos, s)
}

func (txn *Txn) ScheduleBlockUpdate(pos cube.Pos, delay time.Duration) {
	txn.World().scheduleBlockUpdate(pos, delay)
}

func (txn *Txn) HighestLightBlocker(x, z int) int {
	return txn.World().highestLightBlocker(x, z)
}

func (txn *Txn) HighestBlock(x, z int) int {
	return txn.World().highestBlock(x, z)
}

func (txn *Txn) Light(pos cube.Pos) uint8 {
	return txn.World().light(pos)
}

func (txn *Txn) Skylight(pos cube.Pos) uint8 {
	return txn.World().skylight(pos)
}

func (txn *Txn) SetBiome(pos cube.Pos, b Biome) {
	txn.World().setBiome(pos, b)
}

func (txn *Txn) Biome(pos cube.Pos) Biome {
	return txn.World().biome(pos)
}

func (txn *Txn) Temperature(pos cube.Pos) float64 {
	return txn.World().temperature(pos)
}

func (txn *Txn) RainingAt(pos cube.Pos) bool {
	return txn.World().rainingAt(pos)
}

func (txn *Txn) SnowingAt(pos cube.Pos) bool {
	return txn.World().snowingAt(pos)
}

func (txn *Txn) ThunderingAt(pos cube.Pos) bool {
	return txn.World().thunderingAt(pos)
}

func (txn *Txn) AddParticle(pos mgl64.Vec3, p Particle) {
	txn.World().addParticle(pos, p)
}

func (txn *Txn) PlaySound(pos mgl64.Vec3, s Sound) {
	txn.World().playSound(pos, s)
}

func (txn *Txn) AddEntity(e Entity) {
	txn.World().addEntity(e)
}

func (txn *Txn) RemoveEntity(e Entity) {
	txn.World().removeEntity(e)
}

func (txn *Txn) EntitiesWithin(box cube.BBox, ignore func(Entity) bool) []Entity {
	return txn.World().entitiesWithin(box, ignore)
}

func (txn *Txn) Viewers(pos mgl64.Vec3) []Viewer {
	return txn.World().Viewers(pos)
}

// World returns the World of the Txn. It panics if the transaction was already
// marked complete.
func (txn *Txn) World() *World {
	if txn.complete.Load() {
		panic("transaction already complete")
	}
	return txn.w
}
