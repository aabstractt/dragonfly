package entity

import (
	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/internal/nbtconv"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/particle"
	"github.com/df-mc/dragonfly/server/world/sound"
	"github.com/go-gl/mathgl/mgl64"
)

// EnderPearl is a smooth, greenish-blue item used to teleport and to make an eye of ender.
type EnderPearl struct {
	transform
	age   int
	close bool

	owner world.Entity

	c *ProjectileTicker
}

// NewEnderPearl ...
func NewEnderPearl(pos mgl64.Vec3, owner world.Entity) *EnderPearl {
	e := &EnderPearl{c: ProjectileConfig{
		Gravity: 0.03,
		Drag:    0.01,
		HitFunc: teleportOwner,
	}.NewTicker(), owner: owner}
	e.transform = newTransform(e, pos)

	return e
}

// Type returns EnderPearlType.
func (e *EnderPearl) Type() world.EntityType {
	return EnderPearlType{}
}

// Tick ...
func (e *EnderPearl) Tick(w *world.World, current int64) {
	e.c.Tick(e, &e.transform, w, current)
}

// teleporter represents a living entity that can teleport.
type teleporter interface {
	// Teleport teleports the entity to the position given.
	Teleport(pos mgl64.Vec3)
	Living
}

// teleportOwner teleports the owner to a trace.Result's position.
func teleportOwner(res trace.Result, w *world.World, owner world.Entity) {
	if user, ok := owner.(teleporter); ok {
		w.PlaySound(user.Position(), sound.Teleport{})

		user.Teleport(res.Position())

		w.AddParticle(res.Position(), particle.EndermanTeleportParticle{})
		w.PlaySound(res.Position(), sound.Teleport{})

		user.Hurt(5, FallDamageSource{})
	}
}

// New creates an ender pearl with the position, velocity, yaw, and pitch provided. It doesn't spawn the ender pearl,
// only returns it.
func (e *EnderPearl) New(pos, vel mgl64.Vec3, owner world.Entity) world.Entity {
	pearl := NewEnderPearl(pos, owner)
	pearl.vel = vel
	return pearl
}

// Explode ...
func (e *EnderPearl) Explode(explosionPos mgl64.Vec3, impact float64, _ block.ExplosionConfig) {
	e.mu.Lock()
	e.vel = e.vel.Add(e.pos.Sub(explosionPos).Normalize().Mul(impact))
	e.mu.Unlock()
}

// Owner ...
func (e *EnderPearl) Owner() world.Entity {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner
}

// EnderPearlType is a world.EntityType implementation for EnderPearl.
type EnderPearlType struct{}

func (EnderPearlType) EncodeEntity() string { return "minecraft:ender_pearl" }
func (EnderPearlType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.125, 0, -0.125, 0.125, 0.25, 0.125)
}

func (EnderPearlType) DecodeNBT(m map[string]any) world.Entity {
	ep := NewEnderPearl(nbtconv.Vec3(m, "Pos"), nil)
	ep.vel = nbtconv.Vec3(m, "Motion")
	return ep
}

func (EnderPearlType) EncodeNBT(e world.Entity) map[string]any {
	ep := e.(*EnderPearl)
	return map[string]any{
		"Pos":    nbtconv.Vec3ToFloat32Slice(ep.Position()),
		"Motion": nbtconv.Vec3ToFloat32Slice(ep.Velocity()),
	}
}
