package structs

import (
	"math/rand/v2"

	"github.com/pkg/errors"
)

// WoundLevel constants define bleeding severity.
const (
	WoundLevelLight    int32 = iota // Light scratch, minimal bleeding
	WoundLevelModerate              // Moderate wound, steady bleeding
	WoundLevelHeavy                 // Heavy wound, serious bleeding
	WoundLevelCritical              // Critical wound, life-threatening bleeding
)

// Severance describes a severed body part and any resulting wound.
type Severance struct {
	// Wound on the central body part from the stump bleeding.
	// Nil if the severed part couldn't bleed (CanBleed=false).
	Wound *Wound
}

// DamageResult describes the consequences of damage application.
// Only contains information the caller couldn't derive themselves.
type DamageResult struct {
	// Died is true if central health reached 0.
	Died bool

	// Incapacitated is true if a vital body part reached 0 health.
	Incapacitated bool

	// Severance is non-nil if the body part was severed.
	Severance *Severance

	// WoundInflicted is the bleeding wound created by this damage (if any).
	// Nil if no wound was created (damage too low or damage type doesn't bleed).
	WoundInflicted *Wound
}

// CanCombat returns true if the object can participate in combat.
// Combat requires BodyConfigID to be set and MaxHealth > 0.
func (o *Object) CanCombat() bool {
	o.RLock()
	defer o.RUnlock()
	return o.Unsafe.BodyConfigID != "" && o.Unsafe.MaxHealth > 0
}

// IsAlive returns true if the object has positive health.
// Only meaningful for objects that CanCombat(). For non-combat objects
// (where CanCombat() returns false), this will typically return false
// since Health defaults to 0.
func (o *Object) IsAlive() bool {
	o.RLock()
	defer o.RUnlock()
	return o.Unsafe.Health > 0
}

// SetBodyType sets the body type, max health, and initializes body parts.
// This clears any existing body parts and reinitializes them from the config.
// Health is set to maxHealth (full health on body type change).
// Use this from JS via setBodyType("humanoid", 100) in created event handlers.
// Returns an error if the body config is not found.
// This method is thread-safe.
func (o *Object) SetBodyType(ctx Context, bodyConfigID string, maxHealth float32) error {
	if bodyConfigID == "" {
		return errors.New("bodyConfigID cannot be empty")
	}
	if maxHealth <= 0 {
		return errors.New("maxHealth must be positive")
	}

	cfg, ok := ctx.ServerConfig().GetBodyConfig(bodyConfigID)
	if !ok {
		return errors.Errorf("body config %q not found", bodyConfigID)
	}

	o.Lock()
	defer o.Unlock()

	// Set body config and health
	o.Unsafe.BodyConfigID = bodyConfigID
	o.Unsafe.MaxHealth = maxHealth
	o.Unsafe.Health = maxHealth

	// Clear and reinitialize body parts
	o.Unsafe.BodyParts = make(map[string]BodyPartState)
	for partID, partCfg := range cfg.Parts {
		partHealth := float32(partCfg.HealthFraction * float64(maxHealth))
		o.Unsafe.BodyParts[partID] = BodyPartState{
			Health:  partHealth,
			Severed: false,
		}
	}

	// Clear wounds when changing body type
	o.Unsafe.Wounds = nil

	return nil
}

// ClearBodyType removes the body type and all body parts, making the object
// non-combatant. Use this to convert a combat object back to a prop.
// This method is thread-safe.
func (o *Object) ClearBodyType() {
	o.Lock()
	defer o.Unlock()
	o.Unsafe.BodyConfigID = ""
	o.Unsafe.MaxHealth = 0
	o.Unsafe.Health = 0
	o.Unsafe.BodyParts = nil
	o.Unsafe.Wounds = nil
}

// BodyPartsInitialized returns true if body parts have been initialized.
func (o *Object) BodyPartsInitialized() bool {
	o.RLock()
	defer o.RUnlock()
	return len(o.Unsafe.BodyParts) > 0
}

// SelectBodyPart selects a random body part weighted by HitWeight.
// Severed parts are excluded from selection.
// Returns the selected body part ID, or error if no valid parts exist.
// This method is thread-safe.
func (o *Object) SelectBodyPart(ctx Context, rng *rand.Rand) (string, error) {
	o.RLock()
	defer o.RUnlock()

	if o.Unsafe.BodyConfigID == "" {
		return "", errors.New("object has no body config")
	}

	bodyCfg, ok := ctx.ServerConfig().GetBodyConfig(o.Unsafe.BodyConfigID)
	if !ok {
		return "", errors.Errorf("body config %q not found", o.Unsafe.BodyConfigID)
	}

	// Build list of valid (non-severed) parts with their weights
	type weightedPart struct {
		id     string
		weight float64
	}
	var parts []weightedPart
	var totalWeight float64

	for partID, partCfg := range bodyCfg.Parts {
		state, exists := o.Unsafe.BodyParts[partID]
		if !exists || state.Severed {
			continue
		}
		if partCfg.HitWeight > 0 {
			parts = append(parts, weightedPart{partID, partCfg.HitWeight})
			totalWeight += partCfg.HitWeight
		}
	}

	if len(parts) == 0 || totalWeight <= 0 {
		return "", errors.New("no valid body parts to hit")
	}

	// Select based on weighted roll
	roll := rng.Float64()
	target := roll * totalWeight
	cumulative := 0.0
	for _, p := range parts {
		cumulative += p.weight
		if target < cumulative {
			return p.id, nil
		}
	}

	// Fallback to last part (handles floating point edge cases)
	return parts[len(parts)-1].id, nil
}

// findCentralPartID returns the ID of the central body part (typically "torso").
// Central parts are marked with Central=true in the config.
func findCentralPartID(cfg BodyConfig) string {
	for partID, partCfg := range cfg.Parts {
		if partCfg.Central {
			return partID
		}
	}
	return ""
}

// TakeDamage applies damage to a specific body part and central health.
// Returns a DamageResult describing the consequences.
//
// Design:
// - Body part damage = central damage (same amount)
// - Returns error if body part is already severed
// - May cause bleeding (adds to Wounds slice)
// - May cause severing if overkill exceeds threshold
// - When severing: removes wounds from that part, adds critical wound to central part
//
// Parameters:
// - ctx: Context for accessing ServerConfig
// - bodyPartID: Which body part was hit
// - damage: Final damage amount (after armor/blocking; caller handles reduction)
// - damageType: Type of damage (e.g., "slashing", "piercing")
// - at: Timestamp when damage was applied (for wound tracking)
//
// This method is thread-safe.
func (o *Object) TakeDamage(ctx Context, bodyPartID string, damage float32, damageType string, at Timestamp) (*DamageResult, error) {
	if damage <= 0 {
		return &DamageResult{}, nil
	}

	o.Lock()
	defer o.Unlock()

	// Validate combat capability
	if o.Unsafe.BodyConfigID == "" || o.Unsafe.MaxHealth <= 0 {
		return nil, errors.New("object cannot take damage (not a combatant)")
	}

	// Get body config
	bodyCfg, ok := ctx.ServerConfig().GetBodyConfig(o.Unsafe.BodyConfigID)
	if !ok {
		return nil, errors.Errorf("body config %q not found", o.Unsafe.BodyConfigID)
	}

	// Validate body part
	partCfg, ok := bodyCfg.Parts[bodyPartID]
	if !ok {
		return nil, errors.Errorf("body part %q not found in config %q", bodyPartID, o.Unsafe.BodyConfigID)
	}

	// Get current body part state
	partState, ok := o.Unsafe.BodyParts[bodyPartID]
	if !ok {
		return nil, errors.Errorf("body part %q not initialized", bodyPartID)
	}

	// Can't hit an already severed part
	if partState.Severed {
		return nil, errors.Errorf("cannot hit severed body part %q", bodyPartID)
	}

	// Get damage type config
	dmgTypeCfg, _ := ctx.ServerConfig().GetDamageType(damageType)
	// If not found, dmgTypeCfg is zero-valued (no severing, no bleeding)

	result := &DamageResult{}

	// Apply damage to body part and central health (same amount)
	overkill := float32(0)

	// Body part damage
	if damage >= partState.Health {
		overkill = damage - partState.Health
		partState.Health = 0
	} else {
		partState.Health -= damage
	}

	// Central health damage (same amount as body part)
	o.Unsafe.Health -= damage
	if o.Unsafe.Health < 0 {
		o.Unsafe.Health = 0
	}

	// Check for death
	if o.Unsafe.Health <= 0 {
		result.Died = true
	}

	// Check for incapacitation (vital part at 0 health)
	if partCfg.Vital && partState.Health <= 0 {
		result.Incapacitated = true
	}

	// Check for severing
	// Severing occurs when: (overkill * damageType.SeverMult) > (partMaxHealth * SeverThreshold)
	partMaxHealth := float32(partCfg.HealthFraction * float64(o.Unsafe.MaxHealth))
	severThreshold := partMaxHealth * float32(partCfg.SeverThreshold)

	if partCfg.SeverThreshold > 0 && dmgTypeCfg.SeverMult > 0 {
		severDamage := overkill * float32(dmgTypeCfg.SeverMult)
		if severDamage > severThreshold {
			partState.Severed = true
			result.Severance = &Severance{}

			// Severing a vital part is instant death
			if partCfg.Vital {
				result.Died = true
			}

			// Remove wounds from the severed part
			newWounds := make([]Wound, 0, len(o.Unsafe.Wounds))
			for _, w := range o.Unsafe.Wounds {
				if w.BodyPartID != bodyPartID {
					newWounds = append(newWounds, w)
				}
			}
			o.Unsafe.Wounds = newWounds

			// If severed part can bleed, add critical wound to central part
			// (the stump bleeds)
			if partCfg.CanBleed {
				centralPartID := findCentralPartID(bodyCfg)
				if centralPartID != "" {
					severanceWound := Wound{
						BodyPartID: centralPartID,
						Level:      WoundLevelCritical,
						AppliedAt:  at.Uint64(),
						LastTickAt: at.Uint64(),
					}
					o.Unsafe.Wounds = append(o.Unsafe.Wounds, severanceWound)
					result.Severance.Wound = &severanceWound
				}
			}
		}
	}

	// Update body part state
	o.Unsafe.BodyParts[bodyPartID] = partState

	// Check for bleeding (only if not severed and damage type can cause bleeding)
	// Bleeding level based on damage as fraction of max health
	if result.Severance == nil && partCfg.CanBleed && dmgTypeCfg.BleedingMult > 0 {
		// Calculate wound level based on damage severity
		damageFraction := float64(damage) / float64(o.Unsafe.MaxHealth)
		bleedFraction := damageFraction * dmgTypeCfg.BleedingMult

		var woundLevel int32 = -1 // -1 means no wound
		switch {
		case bleedFraction >= 0.20:
			woundLevel = WoundLevelCritical
		case bleedFraction >= 0.10:
			woundLevel = WoundLevelHeavy
		case bleedFraction >= 0.05:
			woundLevel = WoundLevelModerate
		case bleedFraction >= 0.02:
			woundLevel = WoundLevelLight
		}

		if woundLevel >= 0 {
			wound := Wound{
				BodyPartID: bodyPartID,
				Level:      woundLevel,
				AppliedAt:  at.Uint64(),
				LastTickAt: at.Uint64(),
			}
			o.Unsafe.Wounds = append(o.Unsafe.Wounds, wound)
			result.WoundInflicted = &wound
		}
	}

	return result, nil
}

// TotalBleedingLevel returns the sum of all wound levels.
// This method is thread-safe.
func (o *Object) TotalBleedingLevel() int32 {
	o.RLock()
	defer o.RUnlock()
	var total int32
	for _, w := range o.Unsafe.Wounds {
		total += w.Level
	}
	return total
}
