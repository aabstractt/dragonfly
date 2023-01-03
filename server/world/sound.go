package world

import "github.com/go-gl/mathgl/mgl64"

// Sound represents a sound that may be added to the World. When done, viewers of the World may be able to
// hear the sound.
type Sound interface {
	// Play plays the sound. This function may play other sounds too. It is always called when World.playSound
	// is called with the sound.
	Play(w *World, pos mgl64.Vec3)
}
