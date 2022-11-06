package entity

import (
	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/internal/nbtconv"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/particle"
	"github.com/go-gl/mathgl/mgl64"
)

// Snowball is a throwable projectile which damages entities on impact.
type Snowball struct {
	transform
	age   int
	close bool

	owner world.Entity

	c *ProjectileTicker
}

// NewSnowball ...
func NewSnowball(pos mgl64.Vec3, owner world.Entity) *Snowball {
	s := &Snowball{c: ProjectileConfig{
		Gravity: 0.01,
		Drag:    0.01,
		HitFunc: snowballParticles,
	}.NewTicker(), owner: owner}
	s.transform = newTransform(s, pos)

	return s
}

// Type returns SnowballType.
func (s *Snowball) Type() world.EntityType {
	return SnowballType{}
}

// Tick ...
func (s *Snowball) Tick(w *world.World, current int64) {
	s.c.Tick(s, &s.transform, w, current)
}

// snowballParticles spawns 6 particle.SnowballPoof at a trace.Result.
func snowballParticles(res trace.Result, w *world.World, _ world.Entity) {
	for i := 0; i < 6; i++ {
		w.AddParticle(res.Position(), particle.SnowballPoof{})
	}
}

// New creates a snowball with the position, velocity, yaw, and pitch provided. It doesn't spawn the snowball,
// only returns it.
func (s *Snowball) New(pos, vel mgl64.Vec3, owner world.Entity) world.Entity {
	snow := NewSnowball(pos, owner)
	snow.vel = vel
	return snow
}

// Explode ...
func (s *Snowball) Explode(explosionPos mgl64.Vec3, impact float64, _ block.ExplosionConfig) {
	s.mu.Lock()
	s.vel = s.vel.Add(s.pos.Sub(explosionPos).Normalize().Mul(impact))
	s.mu.Unlock()
}

// Owner ...
func (s *Snowball) Owner() world.Entity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner
}

// SnowballType is a world.EntityType implementation for Snowball.
type SnowballType struct{}

func (SnowballType) EncodeEntity() string { return "minecraft:snowball" }
func (SnowballType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.125, 0, -0.125, 0.125, 0.25, 0.125)
}

func (SnowballType) DecodeNBT(m map[string]any) world.Entity {
	s := NewSnowball(nbtconv.Vec3(m, "Pos"), nil)
	s.vel = nbtconv.Vec3(m, "Motion")
	return s
}

func (SnowballType) EncodeNBT(e world.Entity) map[string]any {
	s := e.(*Snowball)
	return map[string]any{
		"Pos":    nbtconv.Vec3ToFloat32Slice(s.Position()),
		"Motion": nbtconv.Vec3ToFloat32Slice(s.Velocity()),
	}
}
