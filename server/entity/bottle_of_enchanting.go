package entity

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/entity/effect"
	"github.com/df-mc/dragonfly/server/internal/nbtconv"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/particle"
	"github.com/df-mc/dragonfly/server/world/sound"
	"github.com/go-gl/mathgl/mgl64"
	"math/rand"
)

// BottleOfEnchanting is a bottle that releases experience orbs when thrown.
type BottleOfEnchanting struct {
	transform
	age   int
	close bool

	owner world.Entity

	c *ProjectileTicker
}

// NewBottleOfEnchanting ...
func NewBottleOfEnchanting(pos mgl64.Vec3, owner world.Entity) *BottleOfEnchanting {
	b := &BottleOfEnchanting{owner: owner, c: ProjectileConfig{
		Gravity: 0.07,
		Drag:    0.01,
		Damage:  -1,
		HitFunc: breakExperienceBottle,
	}.NewTicker()}
	b.transform = newTransform(b, pos)
	return b
}

// Type returns BottleOfEnchantingType.
func (b *BottleOfEnchanting) Type() world.EntityType {
	return BottleOfEnchantingType{}
}

// Glint returns true if the bottle should render with glint. It always returns true.
func (b *BottleOfEnchanting) Glint() bool {
	return true
}

// Tick ...
func (b *BottleOfEnchanting) Tick(w *world.World, current int64) {
	b.c.Tick(b, &b.transform, w, current)
}

// breakExperienceBottle breaks a BottleOfEnchanting at a trace.Result's
// position.
func breakExperienceBottle(res trace.Result, w *world.World, _ world.Entity) {
	colour, _ := effect.ResultingColour(nil)
	w.AddParticle(res.Position(), particle.Splash{Colour: colour})
	w.PlaySound(res.Position(), sound.GlassBreak{})

	for _, orb := range NewExperienceOrbs(res.Position(), rand.Intn(9)+3) {
		orb.SetVelocity(mgl64.Vec3{(rand.Float64()*0.2 - 0.1) * 2, rand.Float64() * 0.4, (rand.Float64()*0.2 - 0.1) * 2})
		w.AddEntity(orb)
	}
}

// New creates a BottleOfEnchanting with the position, velocity, yaw, and pitch provided. It doesn't spawn the
// BottleOfEnchanting, only returns it.
func (b *BottleOfEnchanting) New(pos, vel mgl64.Vec3, owner world.Entity) world.Entity {
	bottle := NewBottleOfEnchanting(pos, owner)
	bottle.vel = vel
	return bottle
}

// Owner ...
func (b *BottleOfEnchanting) Owner() world.Entity {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owner
}

// BottleOfEnchantingType is a world.EntityType for BottleOfEnchanting.
type BottleOfEnchantingType struct{}

func (BottleOfEnchantingType) EncodeEntity() string {
	return "minecraft:xp_bottle"
}
func (BottleOfEnchantingType) BBox(world.Entity) cube.BBox {
	return cube.Box(-0.125, 0, -0.125, 0.125, 0.25, 0.125)
}

func (BottleOfEnchantingType) DecodeNBT(m map[string]any) world.Entity {
	b := NewBottleOfEnchanting(nbtconv.Vec3(m, "Pos"), nil)
	b.vel = nbtconv.Vec3(m, "Motion")
	return b
}

func (BottleOfEnchantingType) EncodeNBT(e world.Entity) map[string]any {
	b := e.(*BottleOfEnchanting)
	return map[string]any{
		"Pos":    nbtconv.Vec3ToFloat32Slice(b.Position()),
		"Motion": nbtconv.Vec3ToFloat32Slice(b.Velocity()),
	}
}
