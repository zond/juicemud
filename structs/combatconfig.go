package structs

// BodyPartConfig defines the properties of a body part in a BodyConfig.
type BodyPartConfig struct {
	// HealthFraction is the fraction of the object's MaxHealth this part has.
	// Normalized at runtime so exact values don't matter, only ratios.
	// Example: torso=0.4, head=0.15, each arm=0.1, each leg=0.125
	HealthFraction float64

	// HitWeight determines how likely this part is to be hit.
	// Normalized at runtime. Severed parts are excluded from calculations.
	HitWeight float64

	// Vital means 0 health = unconscious, severed = death.
	// Typically true for head, torso.
	Vital bool

	// Central means severing produces "cut in half" message instead of "X cut off".
	// Typically true for torso.
	Central bool

	// SeverThreshold is the multiplier for severing check.
	// Severing occurs when: (overkill * damageType.SeverMult) > (partMaxHealth * SeverThreshold)
	// Higher = harder to sever. 0 = cannot be severed.
	SeverThreshold float64

	// CanBleed determines if wounds to this part cause bleeding.
	CanBleed bool

	// CanWield determines if this part can hold weapons/items.
	CanWield bool

	// CanWear determines if this part can wear armor/clothing.
	CanWear bool
}

// BodyConfig defines a body type (humanoid, quadruped, etc.).
type BodyConfig struct {
	// Parts maps body part ID to its configuration.
	// Example: "head", "torso", "leftArm", "rightArm", "leftLeg", "rightLeg"
	Parts map[string]BodyPartConfig
}

// DamageTypeConfig defines properties of a damage type.
type DamageTypeConfig struct {
	// SeverMult is the multiplier for severing checks.
	// 0 = cannot sever (e.g., poison), higher = easier to sever.
	SeverMult float64

	// BleedingMult is the multiplier for bleeding checks.
	// 0 = cannot cause bleeding (e.g., fire cauterizes), higher = more bleeding.
	BleedingMult float64
}

// DefaultBodyConfigs returns the built-in body configurations.
func DefaultBodyConfigs() map[string]BodyConfig {
	return map[string]BodyConfig{
		"humanoid": {
			Parts: map[string]BodyPartConfig{
				"head": {
					HealthFraction: 0.10,
					HitWeight:      0.10,
					Vital:          true,
					Central:        false,
					SeverThreshold: 1.5,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"torso": {
					HealthFraction: 0.30,
					HitWeight:      0.30,
					Vital:          true,
					Central:        true,
					SeverThreshold: 0, // Cannot sever torso
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"leftArm": {
					HealthFraction: 0.10,
					HitWeight:      0.10,
					Vital:          false,
					Central:        false,
					SeverThreshold: 1.0,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"rightArm": {
					HealthFraction: 0.10,
					HitWeight:      0.10,
					Vital:          false,
					Central:        false,
					SeverThreshold: 1.0,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"leftHand": {
					HealthFraction: 0.05,
					HitWeight:      0.05,
					Vital:          false,
					Central:        false,
					SeverThreshold: 0.8,
					CanBleed:       true,
					CanWield:       true,
					CanWear:        true,
				},
				"rightHand": {
					HealthFraction: 0.05,
					HitWeight:      0.05,
					Vital:          false,
					Central:        false,
					SeverThreshold: 0.8,
					CanBleed:       true,
					CanWield:       true,
					CanWear:        true,
				},
				"leftLeg": {
					HealthFraction: 0.10,
					HitWeight:      0.10,
					Vital:          false,
					Central:        false,
					SeverThreshold: 1.0,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"rightLeg": {
					HealthFraction: 0.10,
					HitWeight:      0.10,
					Vital:          false,
					Central:        false,
					SeverThreshold: 1.0,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"leftFoot": {
					HealthFraction: 0.05,
					HitWeight:      0.05,
					Vital:          false,
					Central:        false,
					SeverThreshold: 0.8,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
				"rightFoot": {
					HealthFraction: 0.05,
					HitWeight:      0.05,
					Vital:          false,
					Central:        false,
					SeverThreshold: 0.8,
					CanBleed:       true,
					CanWield:       false,
					CanWear:        true,
				},
			},
		},
	}
}

// DefaultDamageTypes returns the built-in damage type configurations.
func DefaultDamageTypes() map[string]DamageTypeConfig {
	return map[string]DamageTypeConfig{
		"slashing": {SeverMult: 1.0, BleedingMult: 1.0},
		"piercing": {SeverMult: 0.5, BleedingMult: 1.2},
		"bludgeoning": {SeverMult: 0.2, BleedingMult: 0.5},
		"fire": {SeverMult: 0.8, BleedingMult: 0},    // Burns, cauterizes
		"cold": {SeverMult: 0.4, BleedingMult: 0},    // Frostbite, shatter
		"electric": {SeverMult: 0, BleedingMult: 0.3}, // Shock
	}
}
