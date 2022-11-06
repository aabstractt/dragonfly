package entity

import (
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"math"
)

type Projectile interface {
	world.Entity
	Owner() world.Entity
}

type ProjectileConfig struct {
	Gravity float64

	Drag float64

	Damage float64

	HitFunc func(res trace.Result, w *world.World, owner world.Entity)
}

func (conf ProjectileConfig) NewTicker() *ProjectileTicker {
	return &ProjectileTicker{
		MovementComputer: &MovementComputer{
			Gravity:           conf.Gravity,
			Drag:              conf.Drag,
			DragBeforeGravity: true,
		},
		conf: conf,
	}
}

// ProjectileTicker is used to compute movement of a projectile. When constructed, a MovementComputer must be passed.
type ProjectileTicker struct {
	*MovementComputer
	conf  ProjectileConfig
	close bool
	age   int
}

// newProjectileTicker creates a ProjectileTicker with a gravity and drag
// value and if drag should be applied before gravity.
func newProjectileTicker(gravity, drag float64) *ProjectileTicker {
	return &ProjectileTicker{MovementComputer: &MovementComputer{
		Gravity:           gravity,
		Drag:              drag,
		DragBeforeGravity: true,
	}}
}

func (c *ProjectileTicker) Tick(e Projectile, t *transform, w *world.World, current int64) {
	if c.close {
		_ = e.Close()
		return
	}
	t.mu.Lock()
	m, result := c.TickMovement(e, t.pos, t.vel, 0, 0, func(ent world.Entity) bool {
		_, ok := ent.(Living)
		return !ok || ent == e || (c.age < 5 && ent == e.Owner())
	})
	t.pos, t.vel = m.pos, m.vel
	t.mu.Unlock()

	c.age++
	m.Send()

	if m.pos[1] < float64(w.Range()[0]) && current%10 == 0 {
		c.close = true
		return
	}

	if result != nil {
		if r, ok := result.(trace.EntityResult); ok && c.conf.Damage >= 0 {
			if l, ok := r.Entity().(Living); ok {
				src := ProjectileDamageSource{Projectile: e, Owner: e.Owner()}
				if _, vulnerable := l.Hurt(c.conf.Damage, src); vulnerable {
					l.KnockBack(m.pos, 0.45, 0.3608)
				}
			}
		}
		c.conf.HitFunc(result, w, e.Owner())

		c.close = true
	}
}

// TickMovement performs a movement tick on a projectile. Velocity is applied and changed according to the values
// of its Drag and Gravity. A ray trace is performed to see if the projectile has collided with any block or entity,
// the ray trace result is returned.
// The resulting Movement can be sent to viewers by calling Movement.Send.
func (c *ProjectileTicker) TickMovement(e world.Entity, pos, vel mgl64.Vec3, yaw, pitch float64, ignored func(world.Entity) bool) (*Movement, trace.Result) {
	w := e.World()
	viewers := w.Viewers(pos)

	velBefore := vel
	vel = c.applyHorizontalForces(w, pos, c.applyVerticalForces(vel))
	var (
		end = pos.Add(vel)
		hit trace.Result
		ok  bool
	)
	if !mgl64.FloatEqual(end.Sub(pos).LenSqr(), 0) {
		hit, ok = trace.Perform(pos, end, w, e.Type().BBox(e).Grow(1.0), func(e world.Entity) bool {
			g, ok := e.(interface{ GameMode() world.GameMode })
			return (ok && !g.GameMode().HasCollision()) || ignored(e)
		})
	}
	if ok {
		vel = zeroVec3
		end = hit.Position()
	} else {
		yaw, pitch = mgl64.RadToDeg(math.Atan2(vel[0], vel[2])), mgl64.RadToDeg(math.Atan2(vel[1], math.Sqrt(vel[0]*vel[0]+vel[2]*vel[2])))
	}
	c.onGround = ok

	return &Movement{v: viewers, e: e,
		pos: end, vel: vel, dpos: end.Sub(pos), dvel: vel.Sub(velBefore),
		yaw: yaw, pitch: pitch, onGround: c.onGround,
	}, hit
}
