package world

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/entity/damage"
	"github.com/df-mc/dragonfly/server/entity/effect"
	"github.com/df-mc/dragonfly/server/event"
	"github.com/df-mc/dragonfly/server/internal"
	"github.com/df-mc/dragonfly/server/world/sound"
	"github.com/go-gl/mathgl/mgl64"
	"io"
	"math"
	"time"
)

// Entity represents an entity in the world, typically an object that may be moved around and can be
// interacted with by other entities.
// Viewers of a world may view an entity when near it.
type Entity interface {
	io.Closer

	// Name returns a human-readable name for the entity. This is not unique for an entity, but generally
	// unique for an entity type.
	Name() string
	// EncodeEntity converts the entity to its encoded representation: It returns the type of the Minecraft
	// entity, for example 'minecraft:falling_block'.
	EncodeEntity() string

	// BBox returns the bounding box of the Entity.
	BBox() cube.BBox

	// Position returns the current position of the entity in the world.
	Position() mgl64.Vec3
	// Rotation returns the yaw and pitch of the entity in degrees. Yaw is horizontal rotation (rotation around the
	// vertical axis, 0 when facing forward), pitch is vertical rotation (rotation around the horizontal axis, also 0
	// when facing forward).
	Rotation() (yaw, pitch float64)
	// World returns the current world of the entity. This is always the world that the entity can actually be
	// found in.
	World() *World
}

type EntityType interface {
	// EncodeEntity converts the entity type to its encoded representation: It
	// returns the type of the Minecraft entity, for example
	// 'minecraft:falling_block'.
	EncodeEntity() string
	// Components creates a slice of EntityComponents for a new ActualEntity.
	// These components will always be present if an entity of this EntityType
	// is created, but additional components may be added in NewEntity.
	Components(e *ActualEntity) []EntityComponent
	// MaxHealth returns the maximum health of an ActualEntity newly created
	// with this EntityType. This maximum health may be changed afterwards.
	MaxHealth() float64
	// BBox returns the bounding box for the ActualEntity passed of this type.
	BBox(e *ActualEntity) cube.BBox
}

type DamageSource interface {
	// ReducedByArmour checks if the source of damage may be reduced if the
	// receiver of the damage is wearing armour.
	ReducedByArmour() bool
	// ReducedByResistance specifies if the Source is affected by the resistance
	// effect. If false, damage dealt to an entity with this source will not be
	// lowered if the entity has the resistance effect.
	ReducedByResistance() bool
	// Fire specifies if the Source is fire related and should be ignored when
	// an entity has the fire resistance effect.
	Fire() bool
}

type HealingSource interface {
	HealingSource()
}

// DamageModifier is an EntityComponent that can modify damage dealt or taken
// by the entity.
type DamageModifier interface {
	// IgnoresDamageFrom returns true if damage from the DamageSource passed
	// should be ignored entirely. No knockback will be dealt if the entity is
	// attacked and true is returned here. If any of the components return true
	// here, the damage will be ignored entirely.
	IgnoresDamageFrom(src DamageSource) bool
	// ModifyDamageTaken modifies the damage taken by the entity. The initial
	// damage is passed and the resulting damage is returned.
	ModifyDamageTaken(dmg float64, src DamageSource) float64
	// ModifyDamageDealt modifies the damage dealt when attacking another
	// entity. The initial damage is passed and the resulting damage is
	// returned.
	ModifyDamageDealt(dmg float64, src DamageSource) float64
}

type SaveableEntityType interface {
	EntityType

	// DecodeNBT decodes the save data (decoded from NBT) from a map into an
	// ActualEntity.
	DecodeNBT(data map[string]any, e *ActualEntity)
	// EncodeNBT encodes an ActualEntity into a map which can then be encoded as
	// NBT to be written.
	EncodeNBT(e *ActualEntity) map[string]any
}

type EntityComponent interface {
}

type EntityComponentFunc func(e *ActualEntity) EntityComponent

type EntityHandler interface {
	// HandleMove handles the movement of an entity. ctx.Cancel() may be called
	// to cancel the movement event. The new position, yaw and pitch are passed.
	HandleMove(ctx *event.Context, newPos mgl64.Vec3, newYaw, newPitch float64)
	// HandleTeleport handles the teleportation of an entity. ctx.Cancel() may
	// be called to cancel it.
	HandleTeleport(ctx *event.Context, pos mgl64.Vec3)
	// HandleChangeWorld handles when the entity is added to a new world. before
	// may be nil.
	HandleChangeWorld(before, after *World)
	// HandleHeal handles the entity being healed by a healing source.
	// ctx.Cancel() may be called to cancel the healing. The health added may be
	// changed by assigning to *health.
	HandleHeal(ctx *event.Context, health *float64, src HealingSource)
	// HandleHurt handles the entity being hurt by any damage source.
	// ctx.Cancel() may be called to cancel the damage being dealt to the
	// entity. The damage dealt may be changed by assigning to *damage.
	HandleHurt(ctx *event.Context, damage *float64, immunity *time.Duration, src DamageSource)
	// HandleDeath handles the entity dying to a particular DamageSource.
	HandleDeath(src DamageSource)
}

var _ EntityHandler = NopEntityHandler{}

type NopEntityHandler struct{}

func (NopEntityHandler) HandleMove(*event.Context, mgl64.Vec3, float64, float64)           {}
func (NopEntityHandler) HandleTeleport(*event.Context, mgl64.Vec3)                         {}
func (NopEntityHandler) HandleChangeWorld(*World, *World)                                  {}
func (NopEntityHandler) HandleHeal(*event.Context, *float64, HealingSource)                {}
func (NopEntityHandler) HandleHurt(*event.Context, *float64, *time.Duration, DamageSource) {}
func (NopEntityHandler) HandleDeath(DamageSource)                                          {}

type ActualEntity struct {
	t          EntityType
	components []EntityComponent

	mu      internal.RecursivePanicMutex
	handler EntityHandler

	w          *World
	pos, vel   mgl64.Vec3
	yaw, pitch float64

	health, maxHealth float64
	immunity          time.Time
	modifiers         []DamageModifier
}

func NewEntity(t EntityType, extra ...EntityComponentFunc) *ActualEntity {
	health := t.MaxHealth()
	e := &ActualEntity{
		t:         t,
		health:    health,
		maxHealth: health,
	}
	e.components = t.Components(e)
	for _, cf := range extra {
		e.components = append(e.components, cf(e))
	}
	for _, c := range e.components {
		if modifier, ok := c.(DamageModifier); ok {
			e.modifiers = append(e.modifiers, modifier)
		}
	}
	e.Handle(nil)
	return e
}

func (e *ActualEntity) Type() EntityType {
	return e.t
}

func (e *ActualEntity) Position() mgl64.Vec3 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pos
}

func (e *ActualEntity) Rotation() (yaw, pitch float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.yaw, e.pitch
}

func (e *ActualEntity) World() *World {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.w
}

func (e *ActualEntity) Handle(h EntityHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if h == nil {
		h = NopEntityHandler{}
	}
	e.handler = h
}

func (e *ActualEntity) Handler() EntityHandler {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.handler
}

func (e *ActualEntity) Health() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.health
}

func (e *ActualEntity) MaxHealth() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxHealth
}

func (e *ActualEntity) SetMaxHealth(max float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if max <= 0 {
		panic("invalid max health: must be bigger than 0")
	}
	e.maxHealth = max
	e.setHealth(math.Min(e.health, max))
}

func (e *ActualEntity) Dead() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dead()
}

func (e *ActualEntity) dead() bool {
	return e.health <= 0 || mgl64.FloatEqual(e.health, 0)
}

func (e *ActualEntity) Hurt(dmg float64, src DamageSource) (n float64, vulnerable bool) {
	ctx := event.C()
	defer ctx.Finish()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.dead() {
		return 0, false
	}
	for _, mod := range e.modifiers {
		if mod.IgnoresDamageFrom(src) {
			return 0, false
		}
	}

	immunity := time.Second / 2
	if e.handler.HandleHurt(ctx, &dmg, &immunity, src); ctx.Cancelled() {
		return 0, false
	}
	return e.hurt(dmg, src, immunity), true
}

func (e *ActualEntity) hurt(dmg float64, src DamageSource, immunity time.Duration) float64 {
	totalDamage := e.FinalDamageFrom(dmg, src)
	damageLeft := totalDamage

	if a := e.Absorption(); a > 0 {
		if damageLeft > a {
			damageLeft -= a
			e.SetAbsorption(0)
			e.effects.Remove(effect.Absorption{}, p)
		} else {
			e.SetAbsorption(a - damageLeft)
			damageLeft = 0
		}
	}
	e.addHealth(-damageLeft)

	if src.ReducedByArmour() {
		p.Exhaust(0.1)

		armourDamage := int(math.Max(math.Floor(dmg/4), 1))
		for slot, it := range p.armour.Slots() {
			_ = p.armour.Inventory().SetItem(slot, p.damageItem(it, armourDamage))
		}
		p.applyThorns(src)
	}

	for _, viewer := range e.w.Viewers(e.pos) {
		viewer.ViewEntityAction(e, entity.HurtAction{})
	}
	if src.Fire() {
		e.w.PlaySound(e.pos, sound.Burning{})
	} else if _, ok := src.(damage.SourceDrowning); ok {
		e.w.PlaySound(e.pos, sound.Drowning{})
	}

	e.immunity = time.Now().Add(immunity)
	if e.dead() {
		e.kill(src)
	}
	return totalDamage
}

func (e *ActualEntity) Heal(health float64, src HealingSource) {
	ctx := event.C()
	defer ctx.Finish()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.dead() || health < 0 {
		return
	}
	if e.handler.HandleHeal(ctx, &health, src); ctx.Cancelled() {
		return
	}
	e.addHealth(health)
}

func (e *ActualEntity) FinalDamageFrom(dmg float64, src DamageSource) float64 {
	for _, mod := range e.modifiers {
		dmg = mod.ModifyDamageTaken(dmg, src)
	}
	return dmg
}

func (e *ActualEntity) addHealth(health float64) {
	res := math.Min(math.Max(e.health+health, 0), e.maxHealth)
	e.setHealth(res)
}

// TODO: Take care of health sending.
func (e *ActualEntity) setHealth(health float64) {
	e.health = health
}

func As[T EntityComponent](e *ActualEntity) (v T, ok bool) {
	for _, component := range e.components {
		res, ok := component.(T)
		if !ok {
			continue
		}
		return res, true
	}
	return
}

// TickerEntity represents an entity that has a Tick method which should be called every time the entity is
// ticked every 20th of a second.
type TickerEntity interface {
	Entity
	// Tick ticks the entity with the current World and tick passed.
	Tick(w *World, current int64)
}

// SaveableEntity is an Entity that can be saved and loaded with the World it was added to. These entities can be
// registered on startup using RegisterEntity to allow loading them in a World.
type SaveableEntity interface {
	Entity
	NBTer
}

// entities holds a map of name => SaveableEntity to be used for looking up the entity by a string ID. It is registered
// to when calling RegisterEntity.
var entities = map[string]Entity{}

// RegisterEntity registers a SaveableEntity to the map so that it can be saved and loaded with the world.
func RegisterEntity(e Entity) {
	name := e.EncodeEntity()
	if _, ok := entities[name]; ok {
		panic("cannot register the same entity (" + name + ") twice")
	}
	entities[name] = e
}

// EntityByName looks up a SaveableEntity by the name (for example, 'minecraft:slime') and returns it if found.
// EntityByName can only return entities previously registered using RegisterEntity. If not found, the bool returned is
// false.
func EntityByName(name string) (Entity, bool) {
	e, ok := entities[name]
	return e, ok
}

// Entities returns all registered entities.
func Entities() []Entity {
	es := make([]Entity, 0, len(entities))
	for _, e := range entities {
		es = append(es, e)
	}
	return es
}

// EntityAction represents an action that may be performed by an entity. Typically, these actions are sent to
// viewers in a world so that they can see these actions.
type EntityAction interface {
	EntityAction()
}
