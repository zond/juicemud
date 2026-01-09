# Combat System for JuiceMUD

## Overview

A flexible, wizard-configurable combat system that:
- Uses the existing skill system for all calculations
- Has variable attack timing based on weapon speed skill checks
- Implements N-to-M combat (can fight multiple targets who may not fight back)
- Keeps most logic in Go to minimize JS execution
- Is fully wizard-configurable (skills, weapons, armor, damage types)
- Supports optional JS override for all combat messages

## Design Principles

1. **Leverage existing systems**: Use SkillConfig pattern, EffectiveSkills(), Roll(), and skill recharge mechanics (see `docs/skill-system.md` for detailed formulas)
2. **Go-heavy logic**: Combat calculations in Go, JS only for events/customization
3. **Wizard-configurable**: All weapons, armor, damage types defined via configs
4. **Equipment degradation**: Both armor and weapons have health that affects efficacy
5. **Message customization**: All combat messages support optional JS override with Go defaults
6. **Skill recharge balance**: Repeated uses of the same skill suffer cumulative fatigue penalties per the recharge system, preventing spam exploits

---

## Minimal Setup

The combat system works with minimal configuration. Go provides sensible defaults for all components.

### Combat Eligibility

An object can participate in combat (attack or be attacked) only if it has:
- `BodyConfigID` set (references a BodyConfig)
- `MaxHealth > 0`

Objects without these are non-combatants. This means:
- A rock or chair can't be attacked by default
- To make destructible props (cars, doors, furniture), give them a BodyConfig with parts like "frame", "door", "engine"

### Minimum Object Requirements

| Field | Required | Default |
|-------|----------|---------|
| `MaxHealth`, `Health` | Yes | None - must be set |
| `BodyConfigID` | Yes | "humanoid" (if not set, uses default) |
| Skills | No | 0 = poor fighter, but works |
| Weapons | No | Unarmed combat |
| Armor | No | No protection |

**Minimum setup:** Set `BodyConfigID` (or rely on default "humanoid"), `MaxHealth`, and `Health`. Combat works immediately.

### Built-in Defaults

Go provides these defaults when no wizard-defined config exists:

**Default Body Type ("humanoid"):** Head, torso, arms, hands, legs, feet. All can bleed; limbs can be severed. See example in Data Model section.

**Default Damage Types:**
```go
var DefaultDamageTypes = map[string]*DamageTypeConfig{
    "slashing":    {SeverMult: 1.0, BleedingMult: 1.0},  // Swords, axes
    "piercing":    {SeverMult: 0.5, BleedingMult: 1.2},  // Spears, arrows
    "bludgeoning": {SeverMult: 0.2, BleedingMult: 0.5},  // Clubs, hammers
    "fire":        {SeverMult: 0.8, BleedingMult: 0},    // Burns, cauterizes
    "cold":        {SeverMult: 0.4, BleedingMult: 0},    // Frostbite, shatter
    "electric":    {SeverMult: 0,   BleedingMult: 0.3},  // Shock
}
```

**Default CombatConfig:** All tuning constants have sensible defaults (see CombatConfig section).

### Default Combat Messages

All combat events have default message templates. JS callbacks can override for custom flavor.

```go
var DefaultCombatMessages = map[string]string{
    // Attack outcomes - use lang.ThirdPersonSingular(verb) for third person
    // Template functions: {{possessive .Name}} for "John's" vs "James'"
    "attackHit":       "{{.Attacker}} {{.Verb}} {{possessive .Defender}} {{.BodyPart}} for {{.Damage}} damage.",
    "attackMissed":    "{{.Attacker}} {{.Verb}} at {{.Defender}} but misses.",
    "attackParried":   "{{.Defender}} parries {{possessive .Attacker}} attack.",
    "attackDodged":    "{{.Defender}} dodges {{possessive .Attacker}} attack.",
    "attackBlocked":   "{{.Defender}} blocks {{possessive .Attacker}} attack.",

    // Damage
    "damageTaken":      "{{.Object}} takes {{.Damage}} damage to {{.BodyPart}}.",
    "bodyPartDisabled": "{{possessive .Object}} {{.BodyPart}} is disabled!",
    "bodyPartSevered":  "{{possessive .Object}} {{.BodyPart}} is severed!",
    "cutInHalf":        "{{.Object}} is cut in half!",  // Central body part severed

    // Bleeding
    "bleedingStarted": "{{.Object}} starts bleeding {{.Level}}.",
    "bleedingTick":    "{{.Object}} loses {{.Damage}} health from bleeding.",
    "bleedingReduced": "{{possessive .Object}} bleeding subsides.",
    "bleedingStopped": "{{possessive .Object}} bleeding has stopped.",

    // Death/Incapacitation
    "death":       "{{.Object}} has died.",
    "unconscious": "{{.Object}} falls unconscious.",

    // Equipment
    "weaponBroken": "{{possessive .Object}} {{.Weapon}} breaks!",
    "armorBroken":  "{{possessive .Object}} {{.Armor}} is destroyed!",

    // Combat state
    "combatStarted": "{{.Attacker}} attacks {{.Defender}}!",
    "combatEnded":   "Combat has ended.",

    // Ranged
    "reload":         "{{.Object}} reloads {{.Weapon}}.",
    "reloadComplete": "{{.Object}} finishes reloading.",
    "weaponJammed":   "{{possessive .Object}} {{.Weapon}} jams!",
    "unjamStart":     "{{.Object}} starts unjamming {{.Weapon}}.",
    "unjamComplete":  "{{.Object}} clears the jam.",
    "outOfAmmo":      "Click! {{possessive .Object}} {{.Weapon}} is empty.",

    // Aiming
    "aimStart":       "{{.Object}} takes aim at {{.Target}}.",
    "aimInterrupted": "{{possessive .Object}} aim is disrupted.",

    // Grappling
    "grappleStart": "{{.Attacker}} grapples {{.Defender}}!",
    "grappled":     "{{.Object}} is grappled by {{.Grappler}}!",
    "grappleBreak": "{{.Object}} breaks free!",
    "grappleHold":  "{{.Grappler}} tightens their hold on {{.Object}}.",
    "grappleThrow": "{{.Attacker}} throws {{.Defender}}!",

    // Movement/Positioning
    "movementBlocked": "{{.Guard}} blocks {{possessive .Object}} path.",
    "takeCover":       "{{.Object}} takes cover behind {{.Cover}}.",
    "leaveCover":      "{{.Object}} leaves cover.",

    // Flanking
    "outnumbered":       "{{.Object}} is outnumbered!",
    "badlyOutnumbered":  "{{.Object}} is badly outnumbered!",
    "surrounded":        "{{.Object}} is surrounded!",

    // Status effects
    "statusApplied": "{{.Object}} is affected by {{.Effect}}.",
    "statusExpired": "{{.Effect}} wears off from {{.Object}}.",
    "statusTick":    "{{.Object}} suffers from {{.Effect}}.",
}
```

### Message Rendering

```go
// Template functions available in combat messages
var combatTemplateFuncs = template.FuncMap{
    "possessive": lang.Possessive,  // "John" -> "John's", "James" -> "James'"
}

func (g *Game) renderCombatMessage(ctx context.Context, event string, obj *Object, payload map[string]any) string {
    // 1. Try JS callback first (if exists)
    if msg := g.tryJSCallback(obj, "render"+lang.Capitalize(event), payload); msg != "" {
        return msg
    }

    // 2. Fall back to default template
    tmpl := template.Must(template.New(event).Funcs(combatTemplateFuncs).Parse(DefaultCombatMessages[event]))
    var buf bytes.Buffer
    tmpl.Execute(&buf, payload)
    return buf.String()
}
```

### Config Lookup Order

When looking up configs (BodyConfig, DamageTypeConfig, etc.):
1. Check wizard-defined config (by ID in ServerConfig)
2. Fall back to built-in default (if available)
3. If no default exists and config is required, return error

### Levels of Wizard Investment

| Level | Effort | What You Get |
|-------|--------|--------------|
| 0 | Set BodyConfigID + Health | Combat works with defaults, generic messages |
| 1 | Define BodyConfigs | Custom creatures (dragons, robots), destructible objects |
| 2 | Define Weapon/ArmorConfigs | Custom equipment |
| 3 | JS render callbacks | Custom flavor text ("The dragon's fiery maw snaps!") |
| 4 | JS before/after callbacks | Special abilities, custom mechanics |

---

## Data Model

### New ObjectDO Fields

Add to `structs/schema.go`:

```go
type ObjectDO struct {
    // ... existing fields ...

    // Combat stats
    Health    float64  // Current health (0 = incapacitated/dead)
    MaxHealth float64  // Maximum health
    Stamina   float64  // Resource for physical special moves (feint, disarm, power attack)
    MaxStamina float64
    Focus     float64  // Resource for mental actions (spells, aimed shots, intimidate)
    MaxFocus  float64

    // Resource regeneration (lazy computation on access)
    // Uses uint64 timestamps (nanoseconds) matching existing Timestamp pattern
    HealthLastRegenAt  uint64
    StaminaLastRegenAt uint64
    FocusLastRegenAt   uint64

    // Regeneration enable flags (robots don't heal naturally, etc.)
    HealthRegenEnabled  bool  // Default true for organic creatures
    StaminaRegenEnabled bool
    FocusRegenEnabled   bool
    // Note: Stamina/Focus are consumed by wizard-defined JS actions, not core combat.
    // Core combat only tracks and regenerates these resources.

    // Wielded items: bodyPartID -> wielded object ID (one per body part with CanWield)
    // e.g., {"rightArm": "sword1", "leftArm": "shield1"}
    // Two-handed weapons: same object ID in multiple entries
    // e.g., {"rightArm": "greatsword1", "leftArm": "greatsword1"}
    Wielding map[string]string

    // Worn items (armor/clothing): bodyPartID -> ordered list of worn object IDs (innermost first)
    // e.g., {"torso": ["undershirt1", "chainmail1", "plate1"]}
    // Layering validated by thickness/looseness on equip
    Wearing map[string][]string

    // Body part state: bodyPartID -> state (only for objects with BodyConfigID)
    BodyParts map[string]BodyPartState

    // Body and stance (reference global configs)
    BodyConfigID   string  // References BodyConfig (humanoid, quadruped, etc.)
    StanceConfigID string  // References StanceConfig (aggressive, defensive, etc.)

    // Combat state
    Active        bool             // If false, object won't fight, react, etc. (dead, stunned, dormant)
    CombatTargets map[string]bool  // Objects this object is attacking
    CurrentTarget string           // Primary target object ID (for focus)

    // Body part targeting (see Body Part Targeting section)
    FocusBodyPart  string  // Body part attacker is targeting (empty = random by HitWeight)
    DefendBodyPart string  // Body part defender is protecting (empty = no special defense)

    // Status effects (lazily cleaned on access)
    StatusEffects []StatusEffect

    // Bleeding state (managed by Go, not a status effect)
    BleedingLevel int    // 0=none, 1=light, 2=moderate, 3=heavy, 4=critical
    BleedingSince uint64 // When current level started (for natural healing)

    // Cover properties (default 0 = not useful as cover)
    CoverAbsorption      float64  // 0-1, damage absorbed when used as cover
    CoverAccuracyPenalty float64  // Accuracy penalty to hit someone behind this

    // Cover state (for combatants)
    InCoverBehind string  // Object ID providing cover (empty = no cover)

    // Ranged weapon state (for weapon objects)
    CurrentAmmo     int     // Rounds in magazine
    LoadedAmmoType  string  // Which AmmoConfig currently loaded
    Jammed          bool    // Weapon is jammed
    CurrentFireMode string  // Active fire mode ID (empty = use first in FireModes list)

    // Aiming state (for combatants)
    AimingAt    string  // Target object ID being aimed at
    AimingSince uint64  // Timestamp when aiming started (0 = not aiming); bonus computed lazily

    // Grappling state
    GrappledBy string  // Object ID currently grappling this object (empty = not grappled)
    Grappling  string  // Object ID this object is grappling (empty = not grappling)

    // Movement control
    GuardingExit string  // Direction being guarded (empty = not guarding)

    // Room/container effects
    RoomStatusEffects []string  // StatusEffectConfig IDs applied while in this room/container

    // Event tracking (for restart behavior)
    LastEventTime uint64  // Nanoseconds, used to detect first event after restart
}
```

### StatusEffect

Effects that modify combat. Optional timeout allows permanent effects (implants). Optional interval for ticking effects (poison, regen).

```go
type StatusEffect struct {
    ID          string  // Unique ID for this effect instance (generated via juicemud.NextUniqueID())
    ConfigID    string  // References StatusEffectConfig
    AppliedAt   uint64  // Timestamp when effect was applied
    ExpiresAt   uint64  // Timestamp when effect expires (0 = permanent, e.g., implants)

    // State for ticking effects
    LastTickAt  uint64  // Timestamp of last tick event
}
```

**Note:** StatusEffect and BodyPartState will need to be defined as `ctr` types in `structs/schema.benc` for proper serialization.

**Expiry behavior:**
- All status effects are checked lazily on access (e.g., `GetStatusEffects()`)
- Effects with tick intervals are ALSO checked at each tick
- When expired (by lazy check or tick), emit `statusExpired` and clear any interval
- When applied, emit `statusApplied`

**Timing patterns in combat:**
| Pattern | Use Case | Persistence | Examples |
|---------|----------|-------------|----------|
| Intervals | Persistent scheduled events | Survives restart | StatusEffect ticks (poison damage, buff expiry) |
| Goroutines + sleep | Ephemeral timers | Lost on restart | Attack timers, movement delays, reload/unjam |
| Lazy timestamps | Values changing over time | Computed on access | Resource regen, aim bonus, bleeding severity |

### BodyPartState

Tracks the current state of each body part on an object.

```go
type BodyPartState struct {
    Health  float64  // Current health; at 0 = disabled
    Severed bool     // If true, body part has been permanently removed
}
```

**Severing mechanics (Go-managed):**
- Severing check runs automatically after damage is applied
- Requirements: `damageType.SeverMult > 0 && bodyPart.SeverThreshold > 0`
- Threshold: `(overkill * damageType.SeverMult) > (bodyPartMaxHealth * bodyPart.SeverThreshold)`
- Go sets `BodyPartState.Severed = true` and emits `bodyPartSevered` (or `cutInHalf` if Central)
- When severed:
  - Emit `bodyPartSevered` event (different from `bodyPartDisabled`)
  - If `Vital: true`, instant death
  - Wielded items handling:
    - Remove severed body part from `Wielding` map
    - If weapon still has at least one wielding body part: stays wielded with increased difficulty
    - `gripFactor = currentWieldingParts / requiredParts`
    - All weapon challenge levels are divided by gripFactor (making them harder)
    - Example: Two-handed sword with one arm = gripFactor 0.5, so levels ÷ 0.5 = 2x harder
    - If no wielding body parts remain: weapon drops to the ground
  - Any worn armor on that body part drops to the ground
  - The severed body part itself can drop as an object (for gruesome trophies or reattachment)
- Severed parts cannot be healed normally - requires surgical reattachment or magical regeneration
- A severed part is also implicitly disabled (can't attack/defend with what isn't there)

### StatusEffectConfig

```go
type StatusEffectConfig struct {
    ID          string
    Description string

    // Modifiers applied while active (used in combat calculations)
    ChallengeModifiers map[string]float64  // e.g., {"dodge": -10, "damage": 5}

    // Duration (used when applying effect; can be overridden)
    DefaultDuration time.Duration  // Default duration when applied; 0 = permanent

    // Ticking effects
    TickInterval time.Duration  // 0 = no ticking; otherwise emits "statusTick" event

    // Whether this is removable (implants might not be)
    Permanent bool  // If true, no ExpiresAt is set when applied

    // Effect replacement on expiry (handled in Go)
    ReplacedBy string  // StatusEffectConfig ID to apply when this expires (uses that config's DefaultDuration)

    // Stacking behavior
    Unique          bool     // If true, only one instance of this effect can exist; reapplying refreshes duration
    StackAttenuation float64 // Each additional stack's modifiers are multiplied by this (0.5 = 50% effectiveness per stack)
    MaxStacks       int      // Maximum number of stacks (0 = unlimited if not Unique)

    // Movement control (actualDelay = baseDelay / SpeedFactor)
    SpeedFactor     float64  // 0=immobile, 0.5=half speed, 1=normal, 2=double speed
    PreventsActions bool     // Can't attack, use items, etc. (stunned)
}
```

**SpeedFactor examples:**
| Effect | SpeedFactor | PreventsActions | Notes |
|--------|-------------|-----------------|-------|
| stunned | 0 | true | Can't do anything |
| rooted | 0 | false | Can't move, can still fight |
| prone | 0 | false | Must stand up first |
| slowed | 0.5 | false | Half speed (double delay) |
| wading | 0.67 | false | Waist-deep water |
| deep_mud | 0.4 | false | Very slow |
| hasted | 2.0 | false | Double speed (half delay) |
| crippled_leg | 0.55 | false | Injured limb |

**Stacking examples:**
| Effect | Unique | StackAttenuation | MaxStacks | Behavior |
|--------|--------|------------------|-----------|----------|
| stunned | true | - | - | Only one stun at a time; reapplying refreshes duration |
| poison | false | 0.5 | 5 | Up to 5 stacks; each adds 50% of previous stack's damage |
| bleeding_severe | false | 1.0 | 0 | Multiple wounds bleed independently; full effect each |
| rage | false | 1.0 | 3 | Up to 3 stacks; full effect per stack |

### BodyConfig

Defines body structure - what parts can be targeted, their modifiers, equipment slots, and unarmed combat capabilities.

```go
type BodyConfig struct {
    ID          string  // "humanoid", "quadruped", "serpent", etc.
    Description string

    Parts []BodyPart

    // Multi-wielding penalty (wielding separate items in multiple body parts)
    // Skill result mapped via sigmoid to AmbidextrousPenaltyRange in CombatConfig
    // Low skill = accuracy penalty on off-hand attacks; high skill = no penalty
    // Empty = naturally ambidextrous (no penalty), like octopi
    AmbidextrousSkills map[string]bool  // e.g., {"ambidexterity": true}

    // Cover properties (used when taking cover behind creatures with this body type)
    CoverAbsorption      float64  // 0-1, damage absorbed
    CoverAccuracyPenalty float64  // Accuracy penalty to hit someone behind this body

    // Bleeding behavior for this body type (empty BleedingDamagePerTick = no bleeding)
    BleedingDamagePerTick [4]float64   // Health loss per tick for light/moderate/heavy/critical
    BleedingTickInterval  time.Duration // How often bleeding ticks (e.g., 5s)
    BleedingHealTime      time.Duration // Time for bleeding to naturally reduce one level
}

type BodyPart struct {
    ID               string   // "head", "torso", "leftArm", "tail", etc.
    Description      string   // For look output

    // Body part health (tracked in ObjectDO.BodyParts)
    // Actual max health = object.MaxHealth * HealthFraction
    HealthFraction float64  // e.g., 0.3 = this part has 30% of object's total health
    Vital          bool     // If true: 0 health = unconscious, severed = instant death
    Central        bool     // If true: severing = "cut in half" message (torso); false = "X cut off" (limbs)

    // Combat targeting modifiers
    HitWeight        float64  // Relative chance to be hit (torso = 40, head = 10); modified by focus/defend
    DamageMultiplier float64  // Damage multiplier (head = 1.5x)
    CritBonus        float64  // Added to crit chance

    // Can this body part wield items? (hands, tentacles, prehensile tail)
    CanWield bool

    // Grappling effectiveness (0 = can't grapple, 0.4 = human arm, 0.5 = octopus tentacle)
    GrappleEffectiveness float64

    // Unarmed combat (if this body part can attack - e.g., arms can punch, legs can kick)
    // Empty UnarmedDamage = this body part cannot attack unarmed
    UnarmedDamage           map[string]float64  // e.g., {"physical": 5} for fist
    UnarmedSpeedSkills      map[string]bool     // Skills affecting attack speed
    UnarmedAccuracySkills   map[string]bool     // Skills affecting hit chance
    UnarmedDamageBonusSkills map[string]bool    // Skills affecting damage bonus
    UnarmedFocusSkills      map[string]bool     // For targeting specific body parts when unarmed
    UnarmedDefendSkills     map[string]bool     // For protecting specific body parts when unarmed
    UnarmedDescription      string              // e.g., "fist", "claw", "bite"

    // Status effects applied when this body part deals unarmed damage (venom, fire, etc.)
    ApplyStatusEffects []ApplyStatusEffect

    // Unarmed parrying (deflecting attacks without a weapon)
    // Empty map = cannot parry unarmed; non-empty = can attempt to deflect that damage type
    UnarmedParrySkills map[string]map[string]bool  // damage type -> skills for parry
    // Note: No unarmed blocking - blocking requires wielding something

    // Severing threshold - 0 means this body part cannot be severed
    // Severing check: (overkill * damageType.SeverMult) > (bodyPartMaxHealth * SeverThreshold)
    SeverThreshold float64  // e.g., 1.0 = overkill must exceed max health; 0.5 = half that

    // Bleeding - empty/all-zero means this body part cannot bleed (e.g., robot arm, horns)
    // Values are damage thresholds as fraction of this body part's MaxHealth
    // e.g., [0.1, 0.25, 0.5, 0.75] = 10%/25%/50%/75% for light/moderate/heavy/critical
    BleedingDamageThresholds [4]float64
}
```

**Example humanoid body config:**
```go
BodyConfig{
    ID: "humanoid",
    AmbidextrousSkills: map[string]bool{"ambidexterity": true},  // Dual-wield penalty reduced by skill
    BleedingDamagePerTick: [4]float64{1, 2, 4, 8},
    BleedingTickInterval:  5 * time.Second,
    BleedingHealTime:      30 * time.Second,
    Parts: []BodyPart{
        {ID: "head", HealthFraction: 0.12, Vital: true, HitWeight: 8, DamageMultiplier: 1.5, CritBonus: 0.1,
         SeverThreshold: 1.0, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "torso", HealthFraction: 0.30, Vital: true, Central: true, HitWeight: 35, DamageMultiplier: 1.0,
         SeverThreshold: 1.5, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "rightArm", HealthFraction: 0.10, HitWeight: 10, DamageMultiplier: 0.8, GrappleEffectiveness: 0.4,
         SeverThreshold: 0.8, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "leftArm", HealthFraction: 0.10, HitWeight: 10, DamageMultiplier: 0.8, GrappleEffectiveness: 0.4,
         SeverThreshold: 0.8, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "rightHand", HealthFraction: 0.04, HitWeight: 4, DamageMultiplier: 0.6, CanWield: true,
         UnarmedDamage: map[string]float64{"bludgeoning": 3}, UnarmedDescription: "punch",
         SeverThreshold: 0.5, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "leftHand", HealthFraction: 0.04, HitWeight: 4, DamageMultiplier: 0.6, CanWield: true,
         UnarmedDamage: map[string]float64{"bludgeoning": 3}, UnarmedDescription: "punch",
         SeverThreshold: 0.5, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "rightLeg", HealthFraction: 0.10, HitWeight: 10, DamageMultiplier: 0.7, GrappleEffectiveness: 0.1,
         SeverThreshold: 1.0, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "leftLeg", HealthFraction: 0.10, HitWeight: 10, DamageMultiplier: 0.7, GrappleEffectiveness: 0.1,
         SeverThreshold: 1.0, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "rightFoot", HealthFraction: 0.05, HitWeight: 4, DamageMultiplier: 0.5,
         UnarmedDamage: map[string]float64{"bludgeoning": 5}, UnarmedDescription: "kick",
         SeverThreshold: 0.5, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
        {ID: "leftFoot", HealthFraction: 0.05, HitWeight: 4, DamageMultiplier: 0.5,
         UnarmedDamage: map[string]float64{"bludgeoning": 5}, UnarmedDescription: "kick",
         SeverThreshold: 0.5, BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75}},
    },
}
// HealthFraction and HitWeight are normalized at runtime
// Severed body parts are excluded from hit weight calculations
// Grapple power: 2 arms * 0.4 + 2 legs * 0.1 = 1.0
```

**Vital body parts:** Head and torso are `Vital: true`. If health reaches 0, the creature falls unconscious. If severed, instant death.

**Central body parts:** Torso is `Central: true`. Severing produces "cut in half" message instead of "X cut off".

**AmbidextrousSkills:** If empty/nil, creature is naturally ambidextrous (no dual-wield penalty). If set, the skill level determines how much the off-hand penalty is reduced.

**Example dragon body config (natural armor provided via ArmorConfig worn on body parts):**
```go
BodyConfig{
    ID: "dragon",
    AmbidextrousSkills: map[string]bool{"dragonCoordination": true},  // Dragons are fairly coordinated
    Parts: []BodyPart{
        {ID: "head", HealthFraction: 0.15, Vital: true, HitWeight: 10, DamageMultiplier: 2.0, CritBonus: 0.15,
         UnarmedDamage: map[string]float64{"piercing": 30, "fire": 15}, UnarmedDescription: "bite",
         ApplyStatusEffects: []ApplyStatusEffect{
             {StatusEffectID: "burning", Challenge: Challenge{
                 Skills: map[string]bool{"fireBreath": true}, Level: 20,
             }},
         }},
        {ID: "body", HealthFraction: 0.40, Vital: true, Central: true, HitWeight: 50, DamageMultiplier: 1.0},
        {ID: "leftForeclaw", HealthFraction: 0.10, HitWeight: 10, DamageMultiplier: 1.2, CanWield: true,
         UnarmedDamage: map[string]float64{"slashing": 20}, UnarmedDescription: "claw",
         UnarmedParrySkills: map[string]map[string]bool{"slashing": {"clawFighting": true}}},
        // ... more parts: rightForeclaw, wings, tail ...
    },
}
// Dragon's natural armor (scales) is an ArmorConfig pre-equipped on all body parts
```

**Example venomous snake:**
```go
BodyConfig{
    ID: "venomous_snake",
    Parts: []BodyPart{
        {ID: "head", HealthFraction: 0.30, Vital: true, HitWeight: 20, DamageMultiplier: 1.5,
         UnarmedDamage: map[string]float64{"piercing": 5}, UnarmedDescription: "bite",
         ApplyStatusEffects: []ApplyStatusEffect{
             {StatusEffectID: "deadly_venom", Challenge: Challenge{
                 Skills: map[string]bool{"venomStrike": true}, Level: 15,
             }},
         }},
        {ID: "body", HealthFraction: 0.70, Vital: true, Central: true, HitWeight: 80, DamageMultiplier: 1.0},
    },
}
```

**Example octopus body config (naturally ambidextrous):**
```go
BodyConfig{
    ID: "octopus",
    // No AmbidextrousSkills = naturally ambidextrous, no dual-wield penalty
    Parts: []BodyPart{
        {ID: "head", HealthFraction: 0.15, Vital: true, HitWeight: 10, DamageMultiplier: 1.5},
        {ID: "body", HealthFraction: 0.25, Vital: true, Central: true, HitWeight: 30, DamageMultiplier: 1.0},
        {ID: "tentacle1", HealthFraction: 0.075, HitWeight: 7.5, DamageMultiplier: 0.5, CanWield: true,
         UnarmedDamage: map[string]float64{"bludgeoning": 3}, UnarmedDescription: "lash",
         GrappleEffectiveness: 0.5},
        // ... tentacles 2-8, same config ...
    },
}
// 8 tentacles * 0.5 grapple = 4.0 total grapple power
```

### StanceConfig

Stances with skill challenges - players can improve at using stances. Higher skill amplifies positive effects and reduces negative effects.

```go
type StanceConfig struct {
    ID          string  // "aggressive", "defensive", "evasive"
    Description string

    // Modifiers to combat (base values before skill adjustment)
    AccuracyModifier float64  // Added to accuracy rolls
    DamageModifier   float64  // Damage multiplier
    DodgeModifier    float64  // Added to dodge rolls
    ParryModifier    float64
    BlockModifier    float64

    // Skills for this stance
    // Results improve positive modifiers and reduce negative modifiers
    // e.g., aggressive stance has +accuracy but -dodge; higher skill = more accuracy, less dodge penalty
    StanceSkills map[string]bool  // e.g., {"aggressiveStance": true}
}
```

**Stance skill effect:** When applying stance modifiers, the stance skill roll scales them using sigmoid for bounded output:

```go
func applyStanceModifier(baseMod float64, stanceResult float64) float64 {
    // Use sigmoid to map unbounded stanceResult to configurable range
    // Default [0.0, 2.0]: stanceResult=0 → mult=1.0, +∞ → 2.0, -∞ → 0.0
    sigmoid := 1.0 / (1.0 + math.Exp(-stanceResult/config.SigmoidDivisor))
    mult := config.StanceMultRange[0] + sigmoid*(config.StanceMultRange[1]-config.StanceMultRange[0])

    if baseMod >= 0 {
        // Positive modifiers: higher skill = bigger bonus (up to 2x)
        return baseMod * mult
    } else {
        // Negative modifiers: higher skill = smaller penalty (down to 0)
        // Invert the multiplier: mult=2 means penalty reduced to 0
        return baseMod * (2.0 - mult)
    }
}
```

This ensures:
- Bonuses scale from 0x to 2x based on skill
- Penalties scale from 2x to 0x based on skill (never become bonuses)

### ApplyStatusEffect

Defines a status effect that can be applied when damage is dealt (by weapons/ammo/unarmed) or received (by armor).

```go
type ApplyStatusEffect struct {
    StatusEffectID string    // StatusEffectConfig ID to apply
    Challenge      Challenge // Skills and Level for the application check
}
```

**For weapons/ammo/unarmed:** When damage is dealt, `Challenge.Check()` is called for the attacker. If result > 0, the effect is applied to the target.

**For armor:** When the armor absorbs any damage, `Challenge.Check()` is called for the defender. If result > 0, the effect is applied to the attacker.

**Event payload for `statusApplied`:**
```go
{
    StatusEffectID string   // Which effect was applied
    Source         *Object  // Who/what applied it (attacker, weapon, armor object)
    Target         *Object  // Who received the effect
    BodyPart       string   // Which body part was hit (if applicable)
    Damage         float64  // How much damage triggered this
    DamageTypes    map[string]float64  // Breakdown by damage type
    Overkill       float64  // Damage beyond body part max health
}
```

The event payload provides context for JS handlers to customize status effect behavior.

**Note:** Bleeding and severing are Go-managed (see Wound System), not status effects. ApplyStatusEffects is for other effects like poison, burning, curses, etc.

**Examples:**
```go
// Poisoned dagger
WeaponConfig{
    DamageTypes: map[string]float64{"piercing": 8},
    ApplyStatusEffects: []ApplyStatusEffect{
        {StatusEffectID: "weak_poison", Challenge: Challenge{
            Skills: map[string]bool{"poisoning": true}, Level: 15,
        }},
    },
}

// Flaming sword
WeaponConfig{
    DamageTypes: map[string]float64{"slashing": 12, "fire": 5},
    ApplyStatusEffects: []ApplyStatusEffect{
        {StatusEffectID: "burning", Challenge: Challenge{
            Skills: map[string]bool{"fireMagic": true}, Level: 10,
        }},
    },
}

// Cursed armor that drains attacker's stamina
ArmorConfig{
    BaseReduction: map[string]float64{"physical": 0.3},
    ApplyStatusEffects: []ApplyStatusEffect{
        {StatusEffectID: "stamina_drain", Challenge: Challenge{
            Skills: map[string]bool{"darkMagic": true}, Level: 25,
        }},
    },
}
```

### WeaponConfig

Skills are specified as `map[string]bool` - the attacker's effective level across those skills determines the outcome.

```go
type WeaponConfig struct {
    ID          string
    Description string
    Verb        string  // Attack verb for messages: "slash", "stab", "crush", "shoot"
                        // Used with lang.ThirdPersonSingular() for "John slashes you"

    // Equipment slot requirements
    SlotType      string  // Which slot type this uses: "wield" for weapons/shields/tools
    SlotsRequired int     // How many of that slot type needed (1=one-handed, 2=two-handed)

    // Damage by type (e.g., {"physical": 10, "fire": 5} = 10 physical + 5 fire damage)
    DamageTypes  map[string]float64

    // Armor penetration per damage type (0 = none, 0.5 = ignores half, 1.0 = ignores all)
    // Picks, rapiers, armor-piercing weapons have high penetration
    ArmorPenetration map[string]float64

    // Skills affecting combat (each maps skill names to true)
    SpeedSkills       map[string]bool  // e.g., {"agility": true, "swordSpeed": true}
    AccuracySkills    map[string]bool  // For hit chance
    DamageBonusSkills map[string]bool  // For damage bonus
    FocusSkills       map[string]bool  // For targeting specific body parts (rapier: easy, club: hard)
    DefendSkills      map[string]bool  // For protecting specific body parts (shield: easy, dagger: hard)

    // Defense capabilities (per damage type - e.g., shield blocks physical well, not fire)
    // Empty map = cannot parry/block; presence of damage type key = can defend against that type
    ParrySkills map[string]map[string]bool  // damage type -> skills for parry (redirect, no damage)
    BlockSkills map[string]map[string]bool  // damage type -> skills for block (absorb, weapon takes damage)

    // Parry can apply status effect to attacker (staggered, off-balance, disarmed, etc.)
    // Chance scales with parry success via sigmoid: baseChance * sigmoid(parryMargin)
    ParryStatusEffectID       string         // StatusEffectConfig to apply on successful parry (empty = none)
    ParryStatusEffectDuration time.Duration  // How long the effect lasts

    // Status effects applied when this weapon deals damage (poison, burning, etc.)
    // Each effect has a skill check - if attacker passes, effect is applied to target
    ApplyStatusEffects []ApplyStatusEffect

    // Durability (0 = indestructible; blocking damage is 1:1)
    MaxHealth float64  // Maximum weapon health
}
```

**Equipment slot examples:**
| Weapon | SlotType | SlotsRequired | Notes |
|--------|----------|---------------|-------|
| Sword | "wield" | 1 | One-handed |
| Greatsword | "wield" | 2 | Two-handed (needs 2 wield slots across body parts) |
| Shield | "wield" | 1 | One-handed, used for blocking |
| Tower Shield | "wield" | 2 | Massive shield needing both arms |

**Equipping multi-slot weapons:** When SlotsRequired > 1, the system finds that many body parts with the matching slot type and occupies all of them. A 4-armed creature could dual-wield greatswords.

**Equipment health:** Weapon objects use their own `Health` field (from `ObjectDO`) to track durability. When health reaches 0, the weapon is broken - it cannot deal damage, parry, or block. It remains equipped but non-functional until repaired.

**Repair:** Equipment repair is handled by JS - wizards can implement repair NPCs, repair spells, or other mechanisms as needed.

### ArmorConfig

```go
type ArmorConfig struct {
    ID          string
    Description string

    // Which body parts this can be worn on
    CompatibleBodyParts map[string]bool  // e.g., {"head": true}, {"torso": true}

    // Layering: can wear this over existing layers if sum(existing.Thickness) < this.Looseness
    Thickness float64  // How bulky this armor is
    Looseness float64  // How much room inside for layers underneath

    // Protection per damage type: damage type -> base reduction ratio
    // e.g., {"physical": 0.5, "fire": 0.1} = 50% physical reduction, 10% fire reduction
    BaseReduction map[string]float64

    // Skills per damage type (affects armor effectiveness)
    // e.g., {"physical": {"heavyArmor": true}}
    ArmorSkills map[string]map[string]bool

    // Status effects while worn (movement penalty, heat, encumbrance)
    StatusEffects map[string]bool  // StatusEffectConfig IDs applied while wearing

    // Status effects applied when this armor absorbs damage (retaliation, curses, etc.)
    // Each effect has a skill check - if attacker fails to resist, effect is applied to them
    ApplyStatusEffects []ApplyStatusEffect

    // Durability (0 = indestructible; absorbed damage is 1:1)
    MaxHealth float64  // Maximum armor health
}
```

**Armor layering:** Multiple armor pieces can be worn on the same body part if they fit:
```go
func canWearOver(existingLayers []*ArmorConfig, newArmor *ArmorConfig) bool {
    totalThickness := 0.0
    for _, layer := range existingLayers {
        totalThickness += layer.Thickness
    }
    return totalThickness < newArmor.Looseness
}
```

**Example layering:**
| Armor | Thickness | Looseness | Notes |
|-------|-----------|-----------|-------|
| Undershirt | 0.5 | 0.5 | Skin-tight, nothing underneath |
| Chainmail | 2.0 | 3.0 | Can fit undershirt (0.5 < 3.0) |
| Plate cuirass | 3.0 | 6.0 | Can fit undershirt+chainmail (2.5 < 6.0) |

**Armor and body parts:** When a body part is hit, all armor layers worn on that body part provide protection (applied from outermost to innermost). Each layer absorbs damage and takes degradation.

**Equipment health:** Armor objects use their own `Health` field (from `ObjectDO`) to track durability. When health reaches 0, the armor provides no protection. It remains worn but non-functional until repaired.

### DamageTypeConfig

```go
type DamageTypeConfig struct {
    ID          string  // "slashing", "piercing", "bludgeoning", "fire", etc.
    Description string

    // Wound effect multipliers - 0 = can't cause this effect
    // Multiplied to damage before comparing to body part thresholds
    SeverMult    float64  // For severing: higher = easier to sever
    BleedingMult float64  // For bleeding: higher = easier to cause bleeding
}
```

**Example damage types:**
```go
"slashing":    {SeverMult: 1.0, BleedingMult: 1.0}  // Swords, axes - clean cuts bleed freely
"piercing":    {SeverMult: 0.5, BleedingMult: 1.2}  // Spears, arrows - deep wounds bleed well
"bludgeoning": {SeverMult: 0.2, BleedingMult: 0.5}  // Clubs - crushing, internal bleeding
"fire":        {SeverMult: 0.8, BleedingMult: 0}    // Can burn limbs off, cauterizes wounds
"cold":        {SeverMult: 0.4, BleedingMult: 0}    // Frozen limbs can shatter, no bleeding
"electric":    {SeverMult: 0,   BleedingMult: 0.3}  // Shock - some internal bleeding, no severing
```

### CombatConfig

```go
type CombatConfig struct {
    // Attack timing
    MinAttackInterval time.Duration  // e.g., 1s
    MaxAttackInterval time.Duration  // e.g., 10s

    // Base skills for dodge step of defense
    DodgeSkills map[string]bool  // e.g., {"agility": true}

    // Critical hits
    BaseCritChance       float64  // e.g., 0.05 (5%)
    CritDamageMultiplier float64  // e.g., 2.0

    // Resource regeneration (lazy computation)
    HealthRegenSkills       map[string]bool
    HealthRegenPerSecond    float64  // Base rate before skill modifier
    StaminaRegenSkills      map[string]bool
    StaminaRegenPerSecond   float64
    FocusRegenSkills        map[string]bool
    FocusRegenPerSecond     float64
    InCombatRegenMultiplier float64  // e.g., 0.25 (quarter regen during combat)

    // Ranged combat
    RangedDodgePenalty float64          // Penalty to dodge ranged attacks (e.g., -30)
    AimSkills          map[string]bool  // Skill affects aim rate
    AimBonusPerSecond  float64          // Base aim bonus per second (e.g., +10)
    MaxAimBonus        float64          // Cap on aim bonus (e.g., +50)

    // Flanking
    FlankingBonusPerRatio float64  // Bonus per attacker beyond 1:1 (default: 10)
    MaxFlankingBonus      float64  // Cap on flanking bonus (default: 30)

    // Grappling
    GrappleSkills     map[string]bool  // Skills for grapple checks (levels ÷ power)
    GrappleBreakBonus float64          // Bonus to break attempts (defender advantage)

    // Ambush
    AmbushAccuracyBonus float64  // Accuracy bonus when attacking from stealth
    AmbushAutoCrit      bool     // First attack from stealth auto-crits

    // Weapon switching (EquipSkills affects both time and dodge penalty)
    EquipSkills              map[string]bool  // Skill for faster swaps and reduced penalty
    BaseEquipTime            time.Duration    // e.g., 2s (modified by EquipTimeMultRange)
    WeaponSwitchDodgePenalty float64          // e.g., -20 (modified by EquipPenaltyMultRange)

    // Tuning constants (grouped by subsystem, with sane defaults)

    // Sigmoid tuning - affects how quickly skill results reach min/max effects
    SigmoidDivisor float64  // Default: 50; higher = gentler curve

    // Focus/Defend tuning (body part targeting)
    TargetingWeightRange  [2]float64  // Default: [0.1, 2.0] - weight multiplier for focus/defend (min > 0 to avoid div-by-zero)
    TargetingPenaltyRange [2]float64  // Default: [0, 30] - accuracy/defense penalty range

    // Stance tuning
    StanceMultRange [2]float64  // Default: [0.0, 2.0] - stance effect multiplier

    // Ambidextrous tuning (dual-wielding)
    AmbidextrousPenaltyRange [2]float64  // Default: [-30, 0] - off-hand accuracy penalty (low skill = -30, high = 0)

    // Armor tuning
    ArmorSkillMultRange [2]float64  // Default: [0.5, 1.0] - armor effectiveness from skill

    // Regeneration tuning
    RegenMultRange [2]float64  // Default: [0.5, 1.5] - regen rate multiplier

    // Attack speed tuning
    AttackSpeedMultRange [2]float64  // Default: [0.0, 1.0] - maps to min/max attack interval

    // Reload/Unjam tuning
    ReloadMultRange [2]float64  // Default: [0.5, 1.5] - reload time multiplier (lower = faster)
    UnjamMultRange  [2]float64  // Default: [0.5, 1.5] - unjam time multiplier (lower = faster)

    // Aiming tuning
    AimMultRange [2]float64  // Default: [0.5, 1.5] - aim rate multiplier

    // Jam tuning
    JamHealthPenaltyMult float64  // Default: 50 - how much weapon damage affects jamming

    // Weapon switching tuning
    EquipTimeMultRange    [2]float64  // Default: [0.5, 1.5] - equip time multiplier (lower = faster)
    EquipPenaltyMultRange [2]float64  // Default: [0.5, 1.5] - dodge penalty multiplier (lower = less penalty)

    // Status effect limits
    MaxStatusEffects int  // Default: 100 - max status effects per object
}
```

---

## Config Persistence

All global configs are stored in `ServerConfig` (in `structs/serverconfig.go`), which is persisted to the root object's state. ServerConfig has an internal mutex and private fields accessed via thread-safe getters/setters:

```go
type ServerConfig struct {
    mu sync.RWMutex  // Internal mutex for thread-safe access

    // Fields are private, accessed via Get/Set/Delete/Update/Snapshot methods
    spawn               string
    skillConfigs        map[string]SkillConfig
    weaponConfigs       map[string]WeaponConfig
    rangedWeaponConfigs map[string]RangedWeaponConfig
    ammoConfigs         map[string]AmmoConfig
    armorConfigs        map[string]ArmorConfig
    bodyConfigs         map[string]BodyConfig
    stanceConfigs       map[string]StanceConfig
    statusEffectConfigs map[string]StatusEffectConfig
    damageTypes         map[string]DamageTypeConfig
    combatConfig        CombatConfig
    movementConfig      MovementConfig
}
```

### ServerConfig Methods

Each config type has these methods on ServerConfig:

```go
// Read a single config (returns zero value and false if not found)
func (c *ServerConfig) GetWeaponConfig(name string) (WeaponConfig, bool)

// Write a single config
func (c *ServerConfig) SetWeaponConfig(name string, cfg WeaponConfig)

// Delete a config
func (c *ServerConfig) DeleteWeaponConfig(name string)

// Atomic compare-and-swap (for safe concurrent updates from JS)
// old=nil means "insert only if doesn't exist", new=nil means "delete"
func (c *ServerConfig) CompareAndSwapWeaponConfig(name string, old, new *WeaponConfig) bool

// Get snapshot of all configs (defensive copy for serialization)
func (c *ServerConfig) WeaponConfigsSnapshot() map[string]WeaponConfig

// Replace all configs atomically (for bulk loading)
func (c *ServerConfig) ReplaceWeaponConfigs(configs map[string]WeaponConfig)
```

### How Wizards Update Configs

Wizards update configs via JavaScript callbacks. Each config type has `get*Config` and `update*Config` callbacks:

```javascript
// getWeaponConfig(name) -> config or null
const config = getWeaponConfig("longsword");

// updateWeaponConfig(name, oldConfig, newConfig) -> boolean
// - oldConfig null: insert only if key doesn't exist
// - newConfig null: delete the key (if oldConfig matched)
// Returns true if update succeeded, false if current value didn't match oldConfig

// Example: Create a new weapon config
const success = updateWeaponConfig("longsword", null, {
    DamageTypes: {physical: 15.0},
    SpeedSkills: {swords: true, agility: true},
    AccuracySkills: {swords: true},
    // ... other fields
});

// Example: Update existing config (read-modify-write)
const current = getWeaponConfig("longsword");
if (current) {
    const updated = {...current, BaseDamage: 20.0};
    updateWeaponConfig("longsword", current, updated);
}

// Example: Delete a config
const toDelete = getWeaponConfig("longsword");
updateWeaponConfig("longsword", toDelete, null);
```

### Update Callback Implementation Pattern

The Go callbacks (in `game/processing.go`) follow this atomic pattern:

1. **CAS in-memory**: Call `CompareAndSwapWeaponConfig()` on ServerConfig
2. **Persist to storage**: Call `persistServerConfig()` which writes to root object state
3. **Revert on failure**: If persist fails, revert the in-memory change

```go
// Simplified pattern (see updateSkillConfig in processing.go for full implementation)
swapped := g.serverConfig.CompareAndSwapWeaponConfig(name, oldConfig, newConfig)
if !swapped {
    return false  // CAS failed, current value didn't match old
}

if err := g.persistServerConfig(ctx); err != nil {
    // Revert in-memory change on persist failure
    if oldConfig == nil {
        g.serverConfig.DeleteWeaponConfig(name)
    } else {
        g.serverConfig.SetWeaponConfig(name, *oldConfig)
    }
    return false
}
return true
```

### Startup Loading

At server startup, `loadServerConfig()` reads the root object's state and unmarshals it into the in-memory ServerConfig. All config maps are initialized (never nil) so callers can iterate safely.

### JSON Serialization

ServerConfig implements `MarshalJSON`/`UnmarshalJSON` to serialize the private fields:

```json
{
    "Spawn": {"Container": "genesis"},
    "SkillConfigs": {"stealth": {"Forget": 1000000000, "Recharge": 360000000000}},
    "WeaponConfigs": {"longsword": {"BaseDamage": 15.0, ...}},
    ...
}
```

---

## Combat Flow

### Resource Regeneration

Health, Stamina, and Focus regenerate lazily - computed on access, not via timers:

```go
func getResourceWithRegen(ctx Context, current, max float64, lastRegenAt uint64,
                          regenEnabled bool, regenSkills map[string]bool,
                          baseRegenPerSec float64, obj *Object) (newValue float64, newTimestamp uint64) {
    now := ctx.Now()  // Returns structs.Timestamp (uint64 nanoseconds)
    if !regenEnabled || current >= max {
        return current, uint64(now)
    }

    elapsed := now.Time().Sub(structs.Timestamp(lastRegenAt).Time()).Seconds()
    if elapsed <= 0 {
        return current, lastRegenAt
    }

    // Skill affects regen rate via sigmoid (uses config.RegenMultRange)
    result := obj.EffectiveSkills(ctx, regenSkills)
    sigmoid := 1.0 / (1.0 + math.Exp(-result/config.SigmoidDivisor))  // 0 to 1
    mult := config.RegenMultRange[0] + sigmoid*(config.RegenMultRange[1]-config.RegenMultRange[0])

    regenRate := baseRegenPerSec * mult
    newValue = math.Min(max, current + elapsed*regenRate)
    return newValue, uint64(now)
}
```

**Regeneration fields:** See `CombatConfig` above for `HealthRegenSkills`, `HealthRegenPerSecond`, `StaminaRegenSkills`, `StaminaRegenPerSecond`, `FocusRegenSkills`, `FocusRegenPerSecond`, `InCombatRegenMultiplier`.

### Attack Cycle

Combat timing uses simple goroutines with sleep (no queue persistence needed - combat doesn't survive server restarts).

1. **Initiate Combat**: `startCombat(targetID)` in JS
2. **Schedule Attack**: Go spawns goroutine that sleeps for attack interval (calculated via weapon speed skill check)
3. **Attack Resolution**: When goroutine wakes:
   - Check still in combat
   - Check weapon not broken (health > 0)
   - Roll accuracy and crit
   - If hit, run defense chain
   - Apply damage
   - Schedule next attack
4. **End Combat**: `stopCombat(targetID)` or incapacitation

### Attack Resolution (Detailed)

Attacker's **to-hit result** is compared against each defense result. Stance and status effect modifiers are applied throughout.

1. **Weapon/Unarmed Check**:
   - If using equipped weapon and weapon health = 0, attack fails (broken weapon)
   - If **dual-wielding** (weapons in multiple body parts), BOTH weapons attack
   - If no weapon equipped, use **unarmed attacks from ALL body parts** with UnarmedDamage defined
   - Each attacking body part makes a separate attack with its own speed roll
   - Disabled body parts (health = 0) cannot attack

   **Multi-attack balance:** While multi-attack seems powerful:
   - Unarmed damage is much lower than weapons
   - Blocking requires wielding something - no unarmed blocking
   - Skill recharge mechanics (see `docs/skill-system.md`) affect both sides - repeated defenses suffer cumulative fatigue, making later attacks in a barrage more likely to land

   **Multi-attack timing:** Each attacking body part has its own independent attack timer, just like dual-wielding weapons. Each body part rolls its own speed challenges and maintains its own attack cycle. Attacks may land at different times depending on speed rolls - a fast punch might land before a slower kick from the same combatant.

2. **Accuracy Check**:
   - Attacker computes effective accuracy via `EffectiveSkills(ctx, weapon.AccuracySkills)`
   - Add stance's `AccuracyModifier`
   - Add status effect modifiers
   - Subtract focus penalty (if focusing and rolled poorly, see Body Part Targeting)
   - -> `hitScore`

3. **Critical Check** (determined early, applied later if attack lands):
   - Roll against `BaseCritChance + bodyPart.CritBonus`
   - Store `isCrit` flag for later

4. **Parry** (whole attack, weapon or unarmed):
   - Use weapon's `ParrySkills` if weapon equipped and healthy
   - Otherwise use defender's body part `UnarmedParrySkills` (if any)
   - Defender rolls using `Roll(ctx, parrySkills, ...)`
   - Add stance's `ParryModifier`
   - Subtract defend penalty (if defending a body part and rolled poorly, see Body Part Targeting)
   - -> if `parryScore > hitScore`, attack is parried entirely (all damage types deflected)
   - On successful parry with `ParryStatusEffectID` set:
     - Calculate parry margin: `margin = parryScore - hitScore`
     - Apply effect to attacker with probability: `chance = sigmoid(margin / config.SigmoidDivisor)`
   - Failed parry continues to next step

5. **Dodge**:
   - Defender rolls using `Roll(ctx, config.DodgeSkills, ...)`
   - Add stance's `DodgeModifier`
   - Add status effect modifiers
   - Subtract defend penalty (if defending a body part and rolled poorly, see Body Part Targeting)
   - -> if `dodgeScore > hitScore`, attack misses entirely (all remaining damage types)

6. **Block** (whole attack, requires wielding):
   - Skip entirely if parry or dodge succeeded
   - Skip if not wielding anything (no unarmed blocking)
   - Use wielded item's `BlockSkills` if item is healthy (health > 0)
   - Defender rolls using `Roll(ctx, blockSkills, ...)`
   - Add stance's `BlockModifier`
   - Subtract defend penalty (if defending a body part and rolled poorly, see Body Part Targeting)
   - -> if `blockScore > hitScore`, attack is blocked entirely:
     - All damage is negated
     - Wielded item takes damage equal to total blocked amount
   - Failed block continues to next step

7. **Armor Soak** (per damage type, body-part specific):
   - Skip entirely if parry, dodge, or block succeeded
   - Find armor worn on the **hit body part** (from `Wearing` map)
   - If no armor on that body part, or armor health = 0, skip to next step
   - For each damage type in the attack:
     - If `ArmorSkills[damageType]` exists, apply armor skill modifier
     - Use sigmoid to map unbounded result to skill multiplier:
       - `sigmoid = 1 / (1 + exp(-challengeResult/config.SigmoidDivisor))`
       - `skillMult = config.ArmorSkillMultRange[0] + sigmoid * (config.ArmorSkillMultRange[1] - config.ArmorSkillMultRange[0])`
       - Default [0.5, 1.0] maps: very negative → 0.5, zero → 0.75, very positive → 1.0
     - Calculate base reduction: `baseRed = baseReduction * skillMult * (armorHealth / armorMaxHealth)`
     - Apply armor penetration: `finalRed = baseRed * (1 - armorPenetration[damageType])`
     - Reduce damage by `finalRed` (capped at remaining damage)
     - Armor takes damage equal to absorbed amount (1:1)
   - Damage types not in armor's BaseReduction pass through fully

8. **Critical Multiplier**:
   - If `isCrit` (from step 3), apply `CritDamageMultiplier` to remaining damage

9. **Body Part Damage Multiplier**:
   - Apply `bodyPart.DamageMultiplier` to final damage

10. **Apply Damage** (to both body part and central health):
    - Reduce hit body part's health (`BodyParts[bodyPartID].Health`)
    - Calculate overkill: `overkill = max(0, -newBodyPartHealth)`
    - **Body part disabled**: If body part health <= 0 (emit `bodyPartDisabled` event)
    - Reduce defender's central `Health` by same amount

11. **Bleeding Check** (Go-managed):
    - If `damageType.BleedingMult > 0` and body part has `BleedingDamageThresholds`:
      - Calculate `effectiveDamage = damage * damageType.BleedingMult`
      - Calculate `damagePercent = effectiveDamage / bodyPartMaxHealth`
      - Find highest matching threshold level (4=critical down to 1=light)
      - If level > current `BleedingLevel`, upgrade bleeding and emit `bleedingStarted`

12. **Severing Check** (Go-managed):
    - If `damageType.SeverMult > 0` and `bodyPart.SeverThreshold > 0`:
      - If `(overkill * damageType.SeverMult) > (bodyPartMaxHealth * bodyPart.SeverThreshold)`:
        - Set `BodyPartState.Severed = true`
        - Handle weapon drop / grip reduction
        - Emit `bodyPartSevered` (or `cutInHalf` if `Central`)
        - If `bodyPart.Vital`, trigger instant death

13. **Apply Status Effects** (weapons, ammo, unarmed attacks):
    - For each `ApplyStatusEffect` on the damage source:
      - `Challenge.Check()` for the attacker
      - If result > 0, apply the status effect to defender (poison, burning, etc.)
    - For each `ApplyStatusEffect` on defender's armor:
      - `Challenge.Check()` for the defender
      - If result > 0, apply the status effect to **attacker** (thorns, reflection, etc.)

14. **Death Check**:
    - If central Health <= 0:
      - Set `Active = false` (stops fighting, reacting, etc.)
      - Clear `CombatTargets` (stop attacking)
      - Remove this object from all other objects' `CombatTargets` (stop being attacked)
      - Emit `death` event (JS handles respawn, loot, etc.)

### Attack Timing

Uses `EffectiveSkills()` to compute mean skill level, then maps via sigmoid to attack interval:

```go
func calculateAttackInterval(ctx Context, attacker *Object, weapon *WeaponConfig, config *CombatConfig) time.Duration {
    // Compute effective skill level (mean of all speed skills)
    speedResult := attacker.EffectiveSkills(ctx, weapon.SpeedSkills)

    // Higher skill = shorter interval (faster attacks)
    // Use sigmoid to handle unbounded inputs smoothly
    sigmoid := 1.0 / (1.0 + math.Exp(-speedResult/config.SigmoidDivisor))

    interval := config.MaxAttackInterval - time.Duration(
        float64(config.MaxAttackInterval-config.MinAttackInterval) * sigmoid,
    )
    return interval
}
```

---

## Body Part Targeting

Attackers can focus on specific body parts; defenders can protect specific body parts. Both have risk/reward tradeoffs.

### Focus (Attacker)

Set `FocusBodyPart` to target a specific body part. Uses weapon's `FocusSkills` (or `UnarmedFocusSkills` if unarmed).

**Mechanics:**
```go
func applyFocus(ctx Context, attacker *Object, weapon *WeaponConfig, weights map[string]float64, config *CombatConfig) float64 {
    if attacker.FocusBodyPart == "" {
        return 0  // No penalty
    }

    focusResult := attacker.EffectiveSkills(ctx, weapon.FocusSkills)
    sigmoid := 1.0 / (1.0 + math.Exp(-focusResult/config.SigmoidDivisor))

    // Map sigmoid to weight multiplier using TargetingWeightRange
    // sigmoid 0 → min, sigmoid 1 → max
    weightMult := config.TargetingWeightRange[0] + sigmoid*(config.TargetingWeightRange[1]-config.TargetingWeightRange[0])
    weights[attacker.FocusBodyPart] *= weightMult

    // Calculate accuracy penalty when result is negative (sigmoid < 0.5)
    if sigmoid < 0.5 {
        return (0.5 - sigmoid) * 2 * config.TargetingPenaltyRange[1]  // 0 to max penalty
    }
    return 0
}
```

**Effects:**
- Good roll: focused body part more likely to be hit (up to 2x weight)
- Bad roll: focused body part LESS likely (down to 0x weight) AND accuracy penalty

### Defend (Defender)

Set `DefendBodyPart` to protect a specific body part. Uses weapon's `DefendSkills` (or `UnarmedDefendSkills` if unarmed).

**Mechanics:**
```go
func applyDefend(ctx Context, defender *Object, weapon *WeaponConfig, weights map[string]float64, config *CombatConfig) float64 {
    if defender.DefendBodyPart == "" {
        return 0  // No penalty
    }

    defendResult := defender.EffectiveSkills(ctx, weapon.DefendSkills)
    sigmoid := 1.0 / (1.0 + math.Exp(-defendResult/config.SigmoidDivisor))

    // Map sigmoid to weight divisor using TargetingWeightRange
    weightDiv := config.TargetingWeightRange[0] + sigmoid*(config.TargetingWeightRange[1]-config.TargetingWeightRange[0])
    weights[defender.DefendBodyPart] /= weightDiv

    // Calculate defense penalty when result is negative (sigmoid < 0.5)
    if sigmoid < 0.5 {
        return (0.5 - sigmoid) * 2 * config.TargetingPenaltyRange[1]  // 0 to max penalty
    }
    return 0
}
```

**Effects:**
- Good roll: defended body part less likely to be hit (weight ÷ 2)
- Bad roll: defended body part MORE likely (telegraphed!) AND dodge/parry/block penalty

### Body Part Selection

After applying focus and defend modifiers, select body part by weighted random:

```go
func selectBodyPart(attacker, defender *Object, weapon *WeaponConfig, bodyConfig *BodyConfig, config *CombatConfig) (string, float64, float64) {
    weights := map[string]float64{}
    for _, part := range bodyConfig.Parts {
        weights[part.ID] = part.HitWeight
    }

    focusPenalty := applyFocus(attacker, weapon, weights, config)
    defendPenalty := applyDefend(defender, weapon, weights, config)

    return weightedRandomSelect(weights), focusPenalty, defendPenalty
}
```

### Tactical Implications

| Attacker Focus | Defender Defend | Result |
|----------------|-----------------|--------|
| None | None | Random by HitWeight |
| Head | None | Head more likely (if good roll) or accuracy penalty (if bad) |
| None | Head | Head less likely (if good roll) or defense penalty (if bad) |
| Head | Head | Effects partially cancel; both risk penalties |
| Head | Torso | Attacker targets head; defender wasted defense on wrong part |

**Mind games:** Skilled fighters can predict and counter each other's focus/defend choices, creating tactical depth.

---

## Message Rendering

All combat messages support optional JS override. Go provides default messages, but equipment and combatants can customize them via callbacks.

### Message Ownership

| Event | Renderer Object | Rationale |
|-------|-----------------|-----------|
| `renderAttack` | Attacker's **weapon** | Weapon knows its attack style |
| `renderUnarmedAttack` | **Attacker** | Attacker knows their body's attack style |
| `renderMiss` | Attacker's **weapon** (or **attacker** if unarmed) | "Your sword swings wide" / "Your fist misses" |
| `renderDodge` | **Defender** | Defender knows their dodge style |
| `renderParry` | Defender's **weapon** (or **defender** if unarmed) | Weapon/claws did the parrying |
| `renderBlock` | Defender's **weapon** (or **defender** if unarmed) | Weapon/scales did the blocking |
| `renderDamageDealt` | Attacker's **weapon** (or **attacker** if unarmed) | "Your blade cuts deep" / "Your claws tear flesh" |
| `renderDamageReceived` | **Defender** | "Pain sears through you" |
| `renderArmorSoak` | Defender's **armor** | Armor knows its protection style |
| `renderCrit` | Attacker's **weapon** (or **attacker** if unarmed) | Weapon/body knows its crit flavor |
| `renderDeath` | **Dying object** | They know their death style |
| `renderBodyPartDisabled` | **Affected object** | "Your left arm goes limp" |
| `renderStatusApplied` | **Affected object** | "You feel poisoned" |
| `renderStatusTick` | **Affected object** | "The poison burns" |
| `renderStatusExpired` | **Affected object** | "The poison fades" |

### Rendering Chain

1. Identify renderer object (weapon, armor, or combatant per table above)
2. Check for `render<EventType>` callback with `emit` tag
3. If callback exists and returns `{Message: "..."}`, use it
4. Otherwise use Go default message

### Observer Perspective

Each message is rendered per-observer, allowing first/second/third person variants:
- "You hit the goblin" (observer = attacker)
- "The warrior hits you" (observer = defender)
- "The warrior hits the goblin" (observer = bystander)

### Example: Flaming Sword

```javascript
// On a flaming sword weapon object:
addCallback('renderDamageDealt', ['emit'], (req) => {
    if (req.Observer === req.Attacker) {
        return {Message: 'Your flaming blade sears ' + req.DefenderName + '!'};
    } else if (req.Observer === req.Defender) {
        return {Message: 'The flaming sword burns into your flesh!'};
    } else {
        return {Message: req.AttackerName + "'s flaming blade sears " + req.DefenderName + '!'};
    }
});

addCallback('renderCrit', ['emit'], (req) => {
    return {Message: 'The flames explode in a devastating strike!'};
});
```

### Example: Status Effect on Creature

```javascript
// On a creature that can be poisoned:
addCallback('renderStatusApplied', ['emit'], (req) => {
    if (req.ConfigID !== 'poison') return null;  // Let Go handle other effects

    if (req.Observer === getId()) {
        return {Message: 'Venom courses through your veins!'};
    } else {
        return {Message: getName() + ' looks sickly as poison takes hold.'};
    }
});
```

---

## JS Override Callbacks

Beyond message rendering, certain combat events can be intercepted and modified via JS callbacks. These allow wizards to implement special abilities, immunities, and custom mechanics.

### Override Callbacks

| Callback | Object | Can Return | Use Cases |
|----------|--------|------------|-----------|
| `beforeAttack` | Attacker | `{Cancel: true}` | Pacifism effects, attack redirection, resource costs |
| `beforeDamage` | Defender | `{Damage: modified, Cancel: true}` | Damage immunity, absorption shields, damage reflection |
| `beforeDeath` | Dying object | `{Cancel: true}` | Deathward effects, phylacteries, last-stand abilities |
| `beforeStatusApply` | Target | `{Cancel: true}` | Status immunity, poison resistance |

### Example: Deathward

```javascript
// On a creature with deathward buff
addCallback('beforeDeath', ['emit'], (req) => {
    if (hasStatusEffect('deathward')) {
        removeStatusEffect('deathward');
        setHealth(1);  // Survive with 1 HP
        emit('deathwardTriggered', {Object: getId()});
        return {Cancel: true};  // Prevent death
    }
    return null;  // Let death proceed
});
```

### Example: Damage Reflection

```javascript
// On a creature with thorns aura
addCallback('beforeDamage', ['emit'], (req) => {
    if (hasStatusEffect('thorns_aura')) {
        // Reflect 20% of physical damage back to attacker
        const reflected = (req.Damage['physical'] || 0) * 0.2;
        if (reflected > 0) {
            applyDamage(req.Attacker, {'physical': reflected});
        }
    }
    return null;  // Take normal damage
});
```

---

## Wound System

Combat can inflict bleeding and severing wounds, both managed entirely by Go (not status effects).

### Bleeding (Go-managed)

Bleeding is checked automatically after damage is applied. Requirements:
- `damageType.BleedingMult > 0`
- `bodyPart.BleedingDamageThresholds` is non-empty (body part can bleed)
- `bodyConfig.BleedingDamagePerTick` is non-empty (body type can bleed)

**Bleeding levels:** 1=light, 2=moderate, 3=heavy, 4=critical

**Triggering bleeding:**
```go
// After damage to a body part
bodyPartMaxHealth := object.MaxHealth * bodyPart.HealthFraction
effectiveDamage := damage * damageType.BleedingMult
damagePercent := effectiveDamage / bodyPartMaxHealth
for level := 4; level >= 1; level-- {
    if damagePercent >= bodyPart.BleedingDamageThresholds[level-1] {
        if level > defender.BleedingLevel {
            defender.BleedingLevel = level
            defender.BleedingSince = ctx.Now()
            emit("bleedingStarted", {Level: level, BodyPart: partID})
        }
        break
    }
}
```

**Bleeding ticks:** Go schedules ticks at `bodyConfig.BleedingTickInterval`:
```go
// Every tick while BleedingLevel > 0
damage := bodyConfig.BleedingDamagePerTick[defender.BleedingLevel-1]
defender.Health -= damage
emit("bleedingTick", {Level: defender.BleedingLevel, Damage: damage})
if defender.Health <= 0 {
    emit("death", {Cause: "bleeding"})
}
```

**Natural healing:** After `bodyConfig.BleedingHealTime`, bleeding reduces one level:
```go
// Checked on each tick
elapsed := ctx.Now() - defender.BleedingSince
if elapsed >= bodyConfig.BleedingHealTime {
    defender.BleedingLevel--
    defender.BleedingSince = ctx.Now()
    if defender.BleedingLevel > 0 {
        emit("bleedingReduced", {Level: defender.BleedingLevel})
    } else {
        emit("bleedingStopped", {})
    }
}
```

**Medical treatment:** JS can set `BleedingLevel = 0` to stop bleeding immediately (first aid, bandages, etc.).

**Example humanoid body config:**
```go
BodyConfig{
    ID: "humanoid",
    BleedingDamagePerTick: [4]float64{1, 3, 7, 15},  // HP per tick for light/moderate/heavy/critical
    BleedingTickInterval:  5 * time.Second,
    BleedingHealTime:      30 * time.Second,  // 30s to reduce one level
    // ...
}
```

**Example body part thresholds:**
```go
BodyPart{
    ID: "arm", HealthFraction: 0.10,
    BleedingDamageThresholds: [4]float64{0.1, 0.25, 0.5, 0.75},  // 10%/25%/50%/75% of part HP
    SeverThreshold: 1.0,  // Can be severed with 100% overkill
}
BodyPart{
    ID: "robotArm", HealthFraction: 0.12,
    BleedingDamageThresholds: [4]float64{},  // Empty = can't bleed
    SeverThreshold: 1.5,  // Harder to sever (150% overkill needed)
}
```

### Severing (Go-managed)

See **Severing mechanics** in the Body Part System section above. Severing is also Go-managed, not a status effect.

---

## Environmental Effects

Rooms apply status effects to all occupants.

```go
// On room ObjectDO
RoomStatusEffects []string  // StatusEffectConfig IDs applied while in room
```

Effects are applied on entry, removed on exit. Covers combat modifiers, environmental damage, movement penalties, and damage type modifiers (e.g., underwater nullifies fire).

---

## Ranged Combat

Guns, bows, and other ranged weapons. Final damage combines weapon and ammunition.

### RangedWeaponConfig

```go
type RangedWeaponConfig struct {
    ID          string
    Description string

    // Slot requirements
    SlotType      string  // "wield"
    SlotsRequired int     // 1 = pistol, 2 = rifle/bow

    // Range (0 = same room only, 1 = can shoot into adjacent room)
    MaxRange     int      // Most weapons: 0; rifles: 1
    RangePenalty float64  // Accuracy penalty when shooting into adjacent room

    // Point blank modifier (applies when in active melee combat with target)
    // Shotguns/pistols are good up close (+), rifles are awkward (-)
    PointBlankModifier float64

    // Weapon's damage contribution (added to ammo damage)
    // For bows: represents draw strength. For guns: usually empty.
    DamageTypes         map[string]float64
    DamageBonusSkills   map[string]bool

    // Skills for ranged combat
    AccuracySkills  map[string]bool
    FireRateSkills  map[string]bool
    MinFireInterval time.Duration
    MaxFireInterval time.Duration

    // Defense difficulty (penalties to defender - higher = harder to defend)
    // Bows are slow and visible (low penalties); guns are fast (high penalties)
    DodgePenalty float64  // Added to RangedDodgePenalty from CombatConfig
    ParryPenalty float64  // Penalty to parry attempts (guns nearly impossible to parry)
    BlockPenalty float64  // Penalty to block attempts (shields vs bullets)

    // Ammunition
    MagazineSize   int
    ReloadTime     time.Duration    // Base reload time (mechanical)
    ReloadSkills   map[string]bool  // Skill modifier (sigmoid: 0.5x to 1.5x of ReloadTime)
    CompatibleAmmo map[string]bool  // Which AmmoConfig IDs this weapon can use

    // Fire modes
    FireModes []FireModeConfig

    // Reliability
    AvoidJamChallenge Challenge      // Skills + Level for avoiding jams (health penalty added to Level)
    UnjamTime         time.Duration  // Base unjam time (mechanical)
    UnjamSkills       map[string]bool // Skill modifier (sigmoid: 0.5x to 1.5x of UnjamTime)

    // Status effects applied when this weapon deals damage
    ApplyStatusEffects []ApplyStatusEffect

    // Durability (0 = indestructible)
    MaxHealth float64

    // Thrown weapons (daggers, javelins, grenades)
    IsThrown bool  // If true: weapon IS the ammo, consumed on throw, no reload
}
```

**Thrown weapons:** When `IsThrown` is true:
- The weapon object itself is the projectile
- No separate ammunition needed (CompatibleAmmo, MagazineSize ignored)
- Weapon is removed from inventory on throw (can be recovered if it lands somewhere accessible)
- Damage comes from weapon's DamageTypes (no ammo contribution)

```go
// Example: Throwing Dagger
RangedWeaponConfig{
    ID: "throwing_dagger",
    SlotType: "wield", SlotsRequired: 1,
    MaxRange: 1,
    IsThrown: true,
    DamageTypes: map[string]float64{"piercing": 8},
    // No MagazineSize, ReloadTime, CompatibleAmmo needed
}
```

### FireModeConfig

```go
type FireModeConfig struct {
    ID               string   // "single", "burst", "auto"
    ShotsPerTrigger  int      // 1, 3, ~10
    AccuracyModifier float64  // 0, -15, -40
    AmmoPerTrigger   int      // Usually = ShotsPerTrigger
    Description      string
}
```

### AmmoConfig

```go
type AmmoConfig struct {
    ID          string
    Description string

    // Ammo's damage contribution (added to weapon damage)
    DamageTypes       map[string]float64  // e.g., {"piercing": 15}
    DamageBonusSkills map[string]bool

    // Armor penetration per damage type: ignores this fraction of armor/cover absorption
    // 0 = normal, 0.5 = ignores half of absorption, 1.0 = ignores all absorption
    // e.g., {"piercing": 0.3} = AP rounds ignore 30% of armor vs piercing
    ArmorPenetration map[string]float64

    // Status effects applied when this ammo deals damage (burning, poison, etc.)
    // Note: Bleeding/severing are Go-managed based on DamageTypeConfig, not status effects
    ApplyStatusEffects []ApplyStatusEffect
}
```

### Damage Calculation

Final damage = weapon damage + ammo damage (per type):

```go
// Bow (draw strength) + Arrow
weapon.DamageTypes: {"piercing": 5}   // Strong bow
ammo.DamageTypes:   {"piercing": 10}  // Broadhead arrow
total:              {"piercing": 15}

// Gun (no base damage) + Bullet
weapon.DamageTypes: {}                 // Gun adds no damage
ammo.DamageTypes:   {"piercing": 20}   // 9mm round
total:              {"piercing": 20}
```

### Range Model

Simple room-based range:
- **MaxRange 0:** Same room only (pistols, shotguns, SMGs)
- **MaxRange 1:** Same room + adjacent room through any traversable exit (rifles, bows)

No exit configuration needed. If an exit can be walked through, it can be shot through.

### Point Blank

"Point blank" means **in active melee combat** with the target:
- Target has you in their CombatTargets AND is using melee weapons/unarmed, OR
- You have target in your CombatTargets AND are using melee weapons/unarmed

When point blank, PointBlankModifier applies:
- Shotgun (+25): Devastating at arm's length
- Pistol (0): Handles close quarters fine
- Rifle (-15): Awkward, barrel too long
- Sniper (-40): Nearly impossible to aim

### Aiming

Aiming improves accuracy over time. Bonus computed lazily from `AimingSince`, with skill affecting the rate:

```go
func getAimBonus(ctx Context, shooter *Object, config *CombatConfig) float64 {
    if shooter.AimingSince.IsZero() {
        return 0
    }
    elapsed := time.Since(shooter.AimingSince).Seconds()

    // Skill affects aim rate via AimMultRange
    result := shooter.EffectiveSkills(ctx, config.AimSkills)
    sigmoid := 1.0 / (1.0 + math.Exp(-result/config.SigmoidDivisor))
    mult := config.AimMultRange[0] + sigmoid*(config.AimMultRange[1]-config.AimMultRange[0])

    bonus := elapsed * config.AimBonusPerSecond * mult
    return math.Min(bonus, config.MaxAimBonus)
}
```

**CombatConfig for aiming:**
```go
AimSkills         map[string]bool  // Skill affects aim rate (sigmoid: 0.5x to 1.5x)
AimBonusPerSecond float64          // Base aim bonus per second (e.g., +10)
MaxAimBonus       float64          // Cap on aim bonus (e.g., +50)
```

**Aim is broken by:** taking damage, moving, target moving rooms, being grappled, shooting.

### Reload Mechanics

**Slot requirement:** Reloading requires one free wield slot (a free hand to manipulate ammunition). If all wield slots are occupied, you must first unwield something before reloading.

Reload time is primarily mechanical (weapon design) with skill modifier:

```go
func calculateReloadTime(ctx Context, weapon *RangedWeaponConfig, shooter *Object, config *CombatConfig) time.Duration {
    result := shooter.EffectiveSkills(ctx, weapon.ReloadSkills)
    // sigmoid maps result to ReloadMultRange (higher skill = faster = lower multiplier)
    sigmoid := 1.0 / (1.0 + math.Exp(-result/config.SigmoidDivisor))
    // Invert: high sigmoid (good skill) = low mult (fast reload)
    mult := config.ReloadMultRange[1] - sigmoid*(config.ReloadMultRange[1]-config.ReloadMultRange[0])
    return time.Duration(float64(weapon.ReloadTime) * mult)
}
```

### Jam Mechanics

Jamming is skill-based, not random chance:

```go
// Jam check per shot - AvoidJamChallenge with health penalty added to Level
healthPenalty := (1 - weapon.Health/weapon.MaxHealth) * config.JamHealthPenaltyMult
jamChallenge := weapon.AvoidJamChallenge
jamChallenge.Level += healthPenalty  // Damaged weapons jam more easily
if jamChallenge.Check(ctx, shooter, "") < 0 {
    weapon.Jammed = true
}

// Unjam time: base time modified by skill (sigmoid maps to UnjamMultRange)
func calculateUnjamTime(ctx Context, weapon *RangedWeaponConfig, shooter *Object, config *CombatConfig) time.Duration {
    result := shooter.EffectiveSkills(ctx, weapon.UnjamSkills)
    // sigmoid maps result to UnjamMultRange (separate from reload tuning)
    sigmoid := 1.0 / (1.0 + math.Exp(-result/config.SigmoidDivisor))
    // Invert: high sigmoid (good skill) = low mult (fast unjam)
    mult := config.UnjamMultRange[1] - sigmoid*(config.UnjamMultRange[1]-config.UnjamMultRange[0])
    return time.Duration(float64(weapon.UnjamTime) * mult)
}
```

### Ranged Defense Chain

1. **Range Check** - Target within MaxRange rooms?
2. **Accuracy Roll** - `EffectiveSkills(ctx, weapon.AccuracySkills)`
   - Subtract: RangePenalty (if shooting into adjacent room)
   - Add/Subtract: PointBlankModifier (if in active melee with target)
   - Add/Subtract: FireMode.AccuracyModifier
   - Add: Aim bonus
   - Subtract: Target's cover accuracy penalty
   - Subtract: Focus penalty (if focusing on body part, see Body Part Targeting)
3. **Dodge** - Defender rolls with penalties:
   - RangedDodgePenalty (from CombatConfig, base penalty for all ranged)
   - weapon.DodgePenalty (bows: low, guns: high)
4. **Parry** - If defender has parry-capable weapon/unarmed:
   - Apply weapon.ParryPenalty (bows: 20, guns: 80-90)
   - Guns are nearly impossible to parry; arrows can be deflected
5. **Block** - If defender has shield:
   - Apply weapon.BlockPenalty (bows: 10, guns: 20-30)
   - Shields help more vs ranged than parry does
6. **Cover** - If behind cover:
   - Apply armor penetration: `effectiveAbsorption = absorption * (1 - armorPenetration[damageType])`
   - Reduce damage by effectiveAbsorption
7. **Armor Soak** - Normal armor chain (ArmorPenetration reduces effectiveness per damage type)
8. **Apply Damage** - Normal (body part, bleeding, severing)

**Ranged fields:** See `CombatConfig` above for `RangedDodgePenalty`, `AimSkills`, `AimBonusPerSecond`, `MaxAimBonus`.

### Example Weapons

| Weapon | Slots | MaxRange | PointBlank | Magazine | DodgePen | ParryPen | BlockPen |
|--------|-------|----------|------------|----------|----------|----------|----------|
| Pistol | 1 | 0 | 0 | 12 | 30 | 80 | 20 |
| Shotgun | 2 | 0 | +25 | 6 | 25 | 90 | 30 |
| Longbow | 2 | 1 | -30 | 1 | 10 | 20 | 10 |
| Crossbow | 2 | 1 | -15 | 1 | 15 | 40 | 15 |
| Rifle | 2 | 1 | -15 | 30 | 40 | 90 | 25 |

Bows are much easier to parry/block/dodge than guns due to visible flight path and slower projectiles.

---

## Cover System

Objects and creatures can provide cover from ranged attacks.

### Cover Properties

On ObjectDO (default 0 = not useful as cover):
```go
CoverAbsorption      float64  // 0-1, damage absorbed
CoverAccuracyPenalty float64  // Penalty to hit someone behind this
```

On BodyConfig (for using creatures as cover):
```go
CoverAbsorption      float64
CoverAccuracyPenalty float64
```

### Cover Resolution

```go
func getCoverValues(obj *Object) (absorption, penalty float64) {
    // 1. Check object's direct values first
    if obj.CoverAbsorption > 0 || obj.CoverAccuracyPenalty > 0 {
        return obj.CoverAbsorption, obj.CoverAccuracyPenalty
    }
    // 2. Fall back to body type
    if obj.BodyConfigID != "" {
        body := getBodyConfig(obj.BodyConfigID)
        return body.CoverAbsorption, body.CoverAccuracyPenalty
    }
    return 0, 0
}
```

### Cover Mechanics

Both properties scale with cover health:
```go
func getCoverEffectiveness(cover *Object) (absorption, penalty float64) {
    if cover.MaxHealth <= 0 {
        // Indestructible cover (MaxHealth=0) always provides full benefit
        return cover.CoverAbsorption, cover.CoverAccuracyPenalty
    }
    if cover.Health <= 0 {
        // Destroyed cover provides no benefit
        return 0, 0
    }
    ratio := cover.Health / cover.MaxHealth
    return cover.CoverAbsorption * ratio, cover.CoverAccuracyPenalty * ratio
}
```

**Damage to cover:** Cover takes damage equal to absorbed amount (1:1). When cover health = 0, it provides no benefit. Indestructible cover (MaxHealth = 0) never degrades.

### Using Creatures as Cover

You can take cover behind allies (meatshields) but not enemies:

```go
func canUseCover(user, cover *Object) bool {
    // Can't take cover behind someone fighting you
    if cover.CombatTargets[user.ID] {
        return false
    }
    // Can't take cover behind someone you're attacking
    if user.CombatTargets[cover.ID] {
        return false
    }
    return true
}
```

### Example Cover Values

| Object/Body | Absorption | AccuracyPenalty | Notes |
|-------------|------------|-----------------|-------|
| Stone wall | 0.8 | 40 | Very durable |
| Wooden crate | 0.3 | 20 | Splinters quickly |
| Overturned table | 0.2 | 15 | Flimsy |
| humanoid body | 0.2 | 15 | Ally as meatshield |
| dragon body | 0.6 | 35 | Massive creature |

---

## Stealth & Ambush

Stealth uses the existing Description challenge system. An object is "hidden" from an observer when all its descriptions have challenges that observer fails.

### Hiding

Add perception challenge to descriptions:
```go
Descriptions: []Description{
    {Content: "A figure lurks in shadows.",
     Challenge: Challenge{Skills: map[string]bool{"perception": true}, Level: 15}},
}
```

If observer fails the challenge, they don't see the object (existing `look`/movement rendering handles this).

### Ambush

When hidden attacker initiates combat against target who can't see them:
- First attack ignores dodge
- Accuracy bonus (configurable)
- Auto-crit (configurable)

**Attacking removes stealth challenges** from descriptions, making attacker visible to all.

Being seen by a specific observer (they pass perception) only reveals you **to that observer** - challenges remain for others.

---

## Flanking Bonus

When outnumbered in combat, defenders face penalties as they struggle to track multiple attackers.

### Calculation

For each defender, count how many attackers have them as a target vs how many allies are helping:
- `attackers` = count of objects with defender in their CombatTargets
- `defenders` = count of objects with any attacker in their CombatTargets (including self)
- `ratio` = attackers / defenders

### Flanking Effects

| Ratio | Attacker Bonus | Defender Penalty | Description |
|-------|----------------|------------------|-------------|
| 1:1 | 0 | 0 | Even fight |
| 2:1 | +10 accuracy | -10 dodge/parry/block | Outnumbered |
| 3:1 | +20 accuracy | -20 dodge/parry/block | Badly outnumbered |
| 4+:1 | +30 accuracy | -30 dodge/parry/block | Surrounded |

```go
func getFlankingBonus(ratio float64, config *CombatConfig) float64 {
    if ratio <= 1 {
        return 0
    }
    return math.Min(config.MaxFlankingBonus, (ratio-1) * config.FlankingBonusPerRatio)
}
```

### Flanking vs Groups

When groups fight groups:
- Each combatant's ratio is calculated individually
- A 4v2 fight: each defender faces 2:1, each attacker faces 0.5:1
- Tactical positioning matters: focus fire vs spreading damage

**Flanking fields:** See `CombatConfig` above for `FlankingBonusPerRatio`, `MaxFlankingBonus`.

---

## Grappling System

Close-quarters combat for holds, throws, and restraint. Go handles enforcement; JS customizes messages.

### Grapple Power Calculation

**Typical totals:** Humanoid = 1.0 (arms 0.4 each, legs 0.1 each), Giant octopus = 4.0 (8 tentacles at 0.5 each)

Total grapple power = sum of `GrappleEffectiveness` for all body parts where:
- `GrappleEffectiveness > 0`
- Body part is not disabled (health > 0)
- Body part is **not wielding anything** (not present in `Wielding` map)

**Broken weapons:** A wielded item still occupies the body part even if broken (health = 0). Broken weapons reduce grapple power just like functional ones - you're still holding something.

**Zero grapple power:** If total grapple power is 0 (no free grappling limbs, or body has no limbs with GrappleEffectiveness), grappling is impossible. The `grapple` action fails automatically.

**Skill check:** Effective skill is **multiplied by grapple power** before Roll. A humanoid (1.0 power) rolls normally; an octopus (4.0 power) effectively quadruples their skill level; a one-armed human (0.5 power) is at half effectiveness.

```go
// Grapple roll with power multiplier
myEffective := attacker.EffectiveSkills(ctx, config.GrappleSkills)
theirEffective := defender.EffectiveSkills(ctx, config.GrappleSkills)

// Multiply by grapple power for the roll
myRoll := attacker.Roll(ctx, config.GrappleSkills, myEffective * myPower, theirEffective)
theirRoll := defender.Roll(ctx, config.GrappleSkills, theirEffective * theirPower, myEffective)

// NOTE: Pass opposing effective WITHOUT power multiplier for learning.
// Learning should be based on actual skill comparison, not body advantage.
```

**Examples (humanoid: 2 arms @ 0.4, 2 legs @ 0.1 = 1.0 total):**
- Unarmed: 1.0 grapple power (full skill)
- One-handed sword: 0.5 power (one arm + legs)
- Sword + shield: 0.2 power (only legs)
- Two-handed sword: 0.2 power (only legs)

**Note:** Shields are WeaponConfig with `SlotType: "wield"`, so wielding a shield occupies that limb for grappling. Armor (ArmorConfig) does NOT occupy grappling limbs.

### ObjectDO Fields

```go
GrappledBy string  // Object ID currently grappling this object (empty = free)
Grappling  string  // Object ID this object is grappling (empty = not grappling)
```

### Go-Enforced Rules

When grappled (`GrappledBy` is set):
- **Movement blocked** - Cannot leave room
- **Two-handed weapons disabled** - Cannot attack with weapons requiring 2+ slots
- **Dodge ineffective** - Dodge challenges auto-fail; must break free or parry/block
- **Can only target grappler** - Combat actions restricted to the grappler

When grappling (`Grappling` is set):
- **Movement blocked** - Cannot leave while holding someone
- **Grappling limbs occupied** - Limbs contributing to grapple cannot wield weapons

### Grapple Actions

| Action | Effect | Skill Check |
|--------|--------|-------------|
| `grapple` | Initiate grapple | GrappleSkills roll vs target |
| `hold` | Maintain grapple, prevent escape | GrappleSkills roll |
| `choke` | Deal damage over time while holding | GrappleSkills + damage |
| `throw` | Release + knockdown + damage | GrappleSkills roll |
| `break` | Escape from being grappled | GrappleSkills roll vs grappler |
| `reverse` | Escape AND become the grappler | GrappleSkills roll, harder threshold |
| `release` | Voluntarily release target | Automatic |

**Grappling and ambush fields:** See `CombatConfig` above for `GrappleSkills`, `GrappleBreakBonus`, `AmbushAccuracyBonus`, `AmbushAutoCrit`.

---

## Weapon Switching in Combat

Changing weapons mid-combat has tradeoffs similar to movement.

### Core Rules

**While switching weapons:**
- Cannot attack (busy swapping)
- Cannot parry or block (hands occupied)
- Dodge works but with WeaponSwitchDodgePenalty
- Switching time determined by EquipSkills

**Weapon switching fields:** See `CombatConfig` above for `EquipSkills`, `BaseEquipTime`, `WeaponSwitchDodgePenalty`, `EquipTimeMultRange`, `EquipPenaltyMultRange`.

---

## Movement in Combat

Moving while fighting has tradeoffs. No special "flee" state - just movement with combat penalties.

### Core Rules

**While moving in combat:**
- Cannot attack (busy moving)
- Cannot parry or block (not defending position)
- Dodge still works but with CombatMovementDodgePenalty
- Movement speed determined by MovementSkills

**Attacks delay movement:** If you attack while moving, your movement is delayed by the attack duration. This creates tactical tradeoffs:
- Pure flight: move as fast as possible, no offense
- Fighting retreat: slower but dealing damage
- Pure pursuit: catch up quickly, no attacks
- Aggressive pursuit: slower but attacking

### Chase

Chase is implemented in JS. NPCs watch for movement events and follow:

```javascript
addCallback('objectLeftRoom', ['emit'], (event) => {
    if (event.Object === getChaseTarget()) {
        move(event.Direction);  // Following delays our attacks
    }
});
```

This allows wizard-customizable chase behavior: chase range limits, give-up conditions, different AI per NPC type.

### MovementConfig

```go
type MovementConfig struct {
    // Movement timing
    MovementSkills    map[string]bool  // Higher skill = less delay
    BaseMovementDelay time.Duration    // e.g., 2s
    MinMovementDelay  time.Duration    // e.g., 0.5s

    // Combat movement
    CombatMovementDodgePenalty float64  // Penalty to dodge while moving in combat

    // Exit guarding
    GuardSkills map[string]bool  // Skills for guard vs force-through
}
```

Movement delay uses goroutines with sleep. Final delay is `baseDelay / SpeedFactor` (from status effects).

---

## Exit Blocking & Guarding

Objects can guard exits to prevent or challenge passage. One exit per object.

### ObjectDO Fields

```go
GuardingExit string  // Exit direction being guarded (empty = not guarding)
```

### Movement Flow

1. Object attempts to move through exit
2. Check if any object in room has `GuardingExit` matching that direction
3. If guarded: both sides roll using `GuardSkills`
4. If challenger wins: movement proceeds (guard may get free attack)
5. If guard wins: movement blocked, optional message

---

## Implementation Phases

The combat system is implemented in phases, each building on the previous. Each phase should be testable independently before moving to the next.

### Dependency Graph

```
Phase 1: Foundation
         │
         v
Phase 2: ObjectDO Schema
         │
         ├───────────────┬───────────────┐
         v               v               v
Phase 3: Status    Phase 4: Equipment   Phase 5: Resources
         │               │               │
         │               └───────┬───────┘
         │                       v
         │               Phase 6: Basic Melee
         │                       │
         │                       v
         │               Phase 7: Body Parts
         │                       │
         └───────────────┬───────┴───────────────┐
                         v                       v
                 Phase 8: Advanced Melee   Phase 9: Ranged
                         │                       │
                         │                       v
                         │               Phase 10: Aiming & Cover
                         v                       │
                 Phase 11: Movement              │
                         │                       │
                         └───────────┬───────────┘
                                     v
                         Phase 12: Message Rendering
                                     │
                                     v
                         Phase 13: JS Overrides
                                     │
                                     v
                         Phase 14: Polish
```

**Dependency summary:**
- Phases 3, 4, 5: Parallel branches from Phase 2 (independent subsystems)
- Phase 6: Requires Phase 4 (equipment) and Phase 5 (health)
- Phase 7: Requires Phase 6
- Phase 8: Requires Phase 7 and Phase 3 (status effects for wounds)
- Phase 9: Requires Phase 6 (basic combat) and Phase 4 (equipment)
- Phase 10: Requires Phase 9
- Phase 11: Requires Phase 6 (can be done in parallel with 8-10)
- Phases 12-14: Sequential polish after all features complete

---

### Phase 1: Foundation (Config Infrastructure)

**Goal:** All config types defined and accessible via JS. No behavior changes yet.

**Depends on:** Nothing (first phase)

| File | Changes |
|------|---------|
| `structs/combat.go` | New file: All config type definitions |
| `structs/serverconfig.go` | Add maps for each config type, with Get/Set/Delete/CAS/Snapshot/Replace methods |
| `game/processing.go` | Add `get*Config` and `update*Config` JS callbacks for each type |

**Config types to define:**
```go
// Damage system
type DamageTypeConfig struct { ... }

// Melee weapons
type WeaponConfig struct { ... }

// Ranged weapons
type RangedWeaponConfig struct { ... }
type AmmoConfig struct { ... }
type FireModeConfig struct { ... }

// Defense
type ArmorConfig struct { ... }

// Bodies
type BodyConfig struct { ... }
type BodyPart struct { ... }

// Combat modifiers
type StanceConfig struct { ... }
type StatusEffectConfig struct { ... }

// Global tuning
type CombatConfig struct { ... }
type MovementConfig struct { ... }
```

**Testing:**
- [ ] Each config type can be created via `updateXConfig(name, null, config)`
- [ ] Each config type can be read via `getXConfig(name)`
- [ ] Each config type can be updated via `updateXConfig(name, old, new)`
- [ ] Each config type can be deleted via `updateXConfig(name, old, null)`
- [ ] Configs persist across server restart (stored in root object state)

---

### Phase 2: ObjectDO Schema

**Goal:** All combat-related fields on ObjectDO. Just data storage, no logic yet.

**Depends on:** Phase 1 (needs config type definitions)

| File | Changes |
|------|---------|
| `structs/schema.go` | Add all new fields to ObjectDO |
| `structs/combat.go` | Add BodyPartState, StatusEffect types |

**ObjectDO fields to add:**
```go
// Combat stats
Health, MaxHealth float64
Stamina, MaxStamina float64
Focus, MaxFocus float64

// Regeneration timestamps (for lazy computation, nanoseconds)
HealthLastRegenAt, StaminaLastRegenAt, FocusLastRegenAt uint64
HealthRegenEnabled, StaminaRegenEnabled, FocusRegenEnabled bool

// Equipment
Wielding map[string]string      // bodyPartID -> objectID
Wearing  map[string][]string    // bodyPartID -> ordered objectIDs

// Body
BodyParts      map[string]BodyPartState
BodyConfigID   string
StanceConfigID string

// Combat state
CombatTargets  map[string]bool
CurrentTarget  string
FocusBodyPart  string
DefendBodyPart string

// Status effects
StatusEffects []StatusEffect

// Cover
CoverAbsorption, CoverAccuracyPenalty float64
InCoverBehind string

// Ranged state
CurrentAmmo int
LoadedAmmoType string
Jammed bool
CurrentFireMode string
AimingAt string
AimingSince uint64  // Nanoseconds, 0 = not aiming

// Grappling
GrappledBy, Grappling string

// Room effects (for rooms/containers)
RoomStatusEffects []string  // StatusEffectConfig IDs applied while in this room

// Movement
GuardingExit string
```

**Testing:**
- [ ] New fields serialize/deserialize correctly
- [ ] Default values are sensible (empty maps, zero health, etc.)

---

### Phase 3: Status Effects

**Goal:** Complete status effect system, independent of combat.

**Depends on:** Phase 1 (StatusEffectConfig), Phase 2 (StatusEffects field)

| File | Changes |
|------|---------|
| `structs/status.go` | StatusEffect type, GetStatusEffects with lazy cleanup |
| `game/status.go` | Apply/remove logic, tick scheduling |
| `game/processing.go` | JS callbacks for status effects |

**Key behaviors:**
- `GetStatusEffects()` lazily removes expired effects, emits `statusExpired`
- `ApplyStatusEffect(configID, duration)` emits `statusApplied`, schedules tick interval
- `RemoveStatusEffect(id)` emits `statusExpired`, clears any tick interval
- Tick handler checks expiry first, then emits `statusTick` or handles expiry
- Unique effects refresh duration; stacking uses attenuation
- ReplacedBy chain applies on expiry

**JS callbacks:**
- `applyStatusEffect(configID, duration)` / `removeStatusEffect(id)`
- `getStatusEffects()` / `hasStatusEffect(configID)`

**Testing:**
- [ ] Apply effect, verify it appears in getStatusEffects()
- [ ] Effect with duration expires after time passes
- [ ] Permanent effect (duration=0) never expires
- [ ] Ticking effect emits statusTick at intervals
- [ ] Expired ticking effect is cleaned up, interval cancelled
- [ ] Unique effect refreshes duration on reapply
- [ ] Stacking effect respects MaxStacks and StackAttenuation
- [ ] ReplacedBy effect is applied when original expires
- [ ] statusApplied, statusExpired, statusTick events emitted correctly

---

### Phase 4: Equipment System

**Goal:** Wielding weapons, wearing armor, equipment health.

**Depends on:** Phase 1 (WeaponConfig, ArmorConfig), Phase 2 (Wielding/Wearing fields)

| File | Changes |
|------|---------|
| `game/equipment.go` | Equipment logic |
| `game/processing.go` | JS callbacks for equipment |

**Key behaviors:**
- `equip(objectID, bodyPartID)` validates SlotType compatibility, SlotsRequired
- Multi-slot weapons occupy multiple body parts
- Armor layering validates Thickness vs Looseness
- Equipment health tracked on the equipment object itself

**JS callbacks:**
- `equip(objectID, bodyPartID)` / `unequip(bodyPartID)`
- `getEquipped(bodyPartID)` / `getEquipment()`
- `getEquipmentHealth(bodyPartID)`

**Testing:**
- [ ] Equip one-handed weapon to arm
- [ ] Equip two-handed weapon occupies both arms
- [ ] Can't equip weapon to incompatible body part
- [ ] Armor layers correctly (thin under loose)
- [ ] Can't layer armor that doesn't fit
- [ ] Equipment health readable
- [ ] Unequip clears the slot(s)

---

### Phase 5: Resource System

**Goal:** Health, Stamina, Focus with lazy regeneration.

**Depends on:** Phase 2 (resource fields), Phase 1 (CombatConfig for regen rates)

| File | Changes |
|------|---------|
| `game/resources.go` | Regeneration logic |
| `game/processing.go` | JS callbacks for resources |

**Key behaviors:**
- Resources regenerate lazily on access, not via timers
- Skill challenges affect regen rate via sigmoid (RegenMultRange)
- Regen can be disabled per-resource (robots don't heal)
- InCombatRegenMultiplier reduces regen during combat

**JS callbacks:**
- `getHealth()` / `setHealth(n)` / `getMaxHealth()` / `setMaxHealth(n)`
- `getStamina()` / `setStamina(n)` / `getMaxStamina()` / `setMaxStamina(n)`
- `getFocus()` / `setFocus(n)` / `getMaxFocus()` / `setMaxFocus(n)`

**Testing:**
- [ ] Health regenerates over time when damaged
- [ ] Regen respects MaxHealth cap
- [ ] Regen disabled when HealthRegenEnabled=false
- [ ] Skill affects regen rate (higher skill = faster)
- [ ] InCombatRegenMultiplier reduces rate during combat

---

### Phase 6: Basic Melee Combat

**Goal:** Core combat loop with single weapon, basic defense chain.

**Depends on:** Phase 4 (equipment), Phase 5 (health)

| File | Changes |
|------|---------|
| `game/combat.go` | Core combat logic |
| `game/processing.go` | JS callbacks for combat |

**Key behaviors:**
- `startCombat(targetID)` begins attack cycle via goroutine
- `stopCombat(targetID)` cancels attack cycle
- Attack timing via goroutine sleep (not persistent)
- Defense chain: Accuracy → Parry → Dodge → Block → Armor → Damage
- Critical hits determined early, multiplier applied at end
- Death emits `death` event

**Attack flow (simplified, single weapon):**
1. Roll accuracy (using weapon's AccuracySkills)
2. Roll crit (BaseCritChance)
3. Defender parries (whole attack) or it continues
4. Defender dodges (whole attack) or it continues
5. Defender blocks (whole attack, blocker takes damage) or it continues
6. Per damage type: armor soaks damage (armor takes damage)
7. Apply body part multiplier
8. Apply crit multiplier if crit
9. Apply damage to health
10. If health <= 0: emit death

**JS callbacks:**
- `startCombat(targetID)` / `stopCombat(targetID)` / `stopAllCombat()`
- `setCurrentTarget(targetID)` / `getCurrentTarget()`

**JS events:**
- `attackHit`, `attackMissed`, `parried`, `blocked`, `damaged`, `death`, `criticalHit`

**Testing:**
- [ ] startCombat initiates attack cycle
- [ ] Attacks land after speed delay (varies by weapon speed skill)
- [ ] Parry can deflect attack (no damage)
- [ ] Dodge can avoid attack entirely
- [ ] Block absorbs damage, blocking weapon takes damage
- [ ] Armor reduces damage, armor takes damage
- [ ] Crit multiplies damage
- [ ] Death event emitted at 0 health
- [ ] stopCombat ends attack cycle
- [ ] Multiple concurrent combats work (A vs B, A vs C)
- [ ] CombatTargets persists across server restart
- [ ] Combat auto-resumes on restart (goroutines lost, but CombatTargets triggers new cycle)
- [ ] Self-attack prevented

---

### Phase 7: Body Part System

**Goal:** Body part targeting, body part health, Focus/Defend mechanics.

**Depends on:** Phase 6 (combat loop), Phase 1 (BodyConfig)

| File | Changes |
|------|---------|
| `game/combat.go` | Body part targeting |

**Key behaviors:**
- Body part selected by HitWeight (weighted random)
- Focus: attacker targets specific part (risk/reward)
- Defend: defender protects specific part (risk/reward)
- Body part health tracked separately from central health
- Damage applies to BOTH body part and central health
- Disabled body part (health=0) can't attack or defend
- Armor only protects if worn on hit body part

**JS callbacks:**
- `setFocusBodyPart(partID)` / `getFocusBodyPart()` / `clearFocusBodyPart()`
- `setDefendBodyPart(partID)` / `getDefendBodyPart()` / `clearDefendBodyPart()`

**JS events:**
- `bodyPartDisabled`

**Testing:**
- [ ] Attacks land on random body parts by HitWeight
- [ ] Focus increases chance to hit specific part (good roll)
- [ ] Focus causes accuracy penalty (bad roll)
- [ ] Defend decreases chance to hit specific part (good roll)
- [ ] Defend causes defense penalty (bad roll)
- [ ] Body part damage tracked separately from central health
- [ ] Damage applies to BOTH body part AND central health
- [ ] Armor on hit body part applies
- [ ] Armor on other body parts doesn't apply
- [ ] Disabled body part (health=0) can't attack
- [ ] Disabled body part can't defend
- [ ] Vital body part (head/torso) at 0 health causes unconsciousness
- [ ] BodyParts map initialized from BodyConfig
- [ ] bodyPartDisabled event emitted

---

### Phase 8: Advanced Melee

**Goal:** Multi-attack, wounds, stances, flanking, equipment degradation.

**Depends on:** Phase 7 (body parts), Phase 3 (status effects)

| File | Changes |
|------|---------|
| `game/combat.go` | Advanced melee features |

**Key behaviors:**

**Multi-attack:**
- Unarmed: all body parts with UnarmedDamage attack
- Dual-wield: all wielded weapons attack independently
- Each attack has its own speed roll and cycle
- AmbidextrousSkills + AmbidextrousPenaltyRange for multi-wield penalty
- UnarmedParrySkills for parrying without weapon
- No unarmed blocking

**Wounds (Go-managed):**
- Bleeding: checked after damage based on DamageTypeConfig.BleedingMult and body part thresholds
- Severing: checked after damage based on DamageTypeConfig.SeverMult and overkill vs body part SeverThreshold
- Other effects (poison, burning): via ApplyStatusEffects on weapons/ammo/body parts

**Stances:**
- StanceConfig modifiers apply to combat rolls
- StanceSkills scale modifier effectiveness

**Flanking:**
- Count attackers vs defenders
- FlankingBonusPerRatio applies accuracy bonus
- Capped at MaxFlankingBonus

**Equipment degradation:**
- Weapons at 0 health can't attack/parry/block
- Armor at 0 health provides no protection
- Equipment remains equipped until removed

**Status modifiers:**
- ChallengeModifiers from active effects apply to combat rolls
- SpeedFactor affects attack timing
- PreventsActions blocks attacks

**Stealth & Ambush:**
- Hidden attackers (all descriptions have challenges observer fails) get ambush bonuses
- AmbushAccuracyBonus added to first attack from stealth
- AmbushAutoCrit makes first attack auto-crit
- Ambush ignores dodge on first attack
- Attacking removes stealth (clears description challenges)

**JS callbacks:**
- `setStance(stanceConfigID)` / `getStance()`

**Testing:**
- [ ] Unarmed attacks from all capable body parts
- [ ] Dual-wield attacks with both weapons
- [ ] Ambidexterity penalty applies
- [ ] Unarmed parry works with skill
- [ ] Can't block unarmed
- [ ] Bleeding applied by damage percent
- [ ] Severing on overkill damage
- [ ] Vital part sever = death
- [ ] Grip factor degrades weapon after partial sever
- [ ] Stance modifiers affect rolls
- [ ] Flanking bonus applies when outnumbered
- [ ] Broken weapon can't attack
- [ ] Status effects modify combat rolls
- [ ] Ambush from stealth ignores dodge
- [ ] AmbushAccuracyBonus applies on first attack
- [ ] Attacking breaks stealth

---

### Phase 9: Ranged Combat

**Goal:** Guns, bows, ammo, reload, jam mechanics.

**Depends on:** Phase 6 (basic combat), Phase 4 (equipment)

| File | Changes |
|------|---------|
| `game/ranged.go` | Ranged combat logic |
| `game/processing.go` | JS callbacks for ranged |

**Key behaviors:**
- RangedWeaponConfig defines ranged weapons
- AmmoConfig defines ammunition
- Damage = weapon damage + ammo damage
- Fire modes (single, burst, auto) affect accuracy/ammo
- Magazine tracks CurrentAmmo
- Reload requires free wield slot, takes skill-based time
- Jam on bad skill roll, unjam takes skill-based time
- Ranged defense penalties (DodgePenalty, ParryPenalty, BlockPenalty)
- Thrown weapons (IsThrown) consume the weapon itself

**JS callbacks:**
- `setFireMode(modeID)` / `getFireMode()`
- `reload()` / `unjam()`

**Testing:**
- [ ] Ranged attack deals weapon+ammo damage
- [ ] Fire mode affects accuracy and ammo consumption
- [ ] Out of ammo prevents firing
- [ ] Reload fills magazine
- [ ] Reload requires free hand
- [ ] Jam prevents firing
- [ ] Unjam clears jam
- [ ] Ranged defense penalties apply (harder to dodge/parry)
- [ ] Thrown weapon consumed on throw

---

### Phase 10: Aiming & Cover

**Goal:** Aiming bonus, range model, cover system.

**Depends on:** Phase 9 (ranged combat)

| File | Changes |
|------|---------|
| `game/ranged.go` | Aiming and cover logic |
| `game/processing.go` | JS callbacks |

**Key behaviors:**

**Aiming:**
- Aim bonus accumulates over time (lazy from AimingSince)
- AimSkills affect aim rate via sigmoid (EffectiveSkills → scaled modifier)
- Capped at MaxAimBonus
- Broken by: damage, movement, target moves, firing

**Range:**
- MaxRange 0 = same room only
- MaxRange 1 = can shoot into adjacent room
- RangePenalty applies for adjacent room shots
- PointBlankModifier when in melee with target

**Cover:**
- CoverAbsorption, CoverAccuracyPenalty on objects/bodies
- InCoverBehind field tracks cover object
- Cover takes damage equal to absorbed (degrades 1:1)
- Can't cover behind enemies
- ArmorPenetration reduces cover effectiveness

**JS callbacks:**
- `startAiming(targetID)` / `stopAiming()` / `getAimBonus()`
- `takeCover(objectID)` / `leaveCover()`

**Testing:**
- [ ] Aim bonus increases over time
- [ ] Aim rate affected by skill
- [ ] Aim capped at max
- [ ] Aim broken by damage/movement
- [ ] Can shoot into adjacent room (MaxRange 1)
- [ ] Range penalty applies
- [ ] Point blank modifier applies in melee
- [ ] Cover reduces accuracy
- [ ] Cover absorbs damage
- [ ] Cover degrades from damage
- [ ] Can't cover behind enemy

---

### Phase 11: Movement & Grappling

**Goal:** Tactical movement, exit guarding, grapple system.

**Depends on:** Phase 6 (combat), Phase 1 (MovementConfig)

| File | Changes |
|------|---------|
| `game/movement.go` | Combat movement and grappling |

**Key behaviors:**

**Movement in combat:**
- CombatMovementDodgePenalty while moving
- Can't attack/parry/block while moving
- Attacks delay movement completion
- SpeedFactor from status effects modifies delay

**Exit guarding:**
- GuardingExit field specifies direction
- GuardSkills for contested passage (both sides roll)
- Guard can get free attack on forced passage

**Grappling:**
- Grapple power = sum of free GrappleEffectiveness
- Effective skill multiplied by grapple power (octopus with 4.0 power has 4x effective skill)
- Grappled: can't move, dodge fails, can only target grappler
- Grappler: can't move, grappling limbs occupied
- Actions: grapple, hold, choke, throw, break, reverse, release

**Testing:**
- [ ] Movement in combat has dodge penalty
- [ ] Can't attack while moving
- [ ] SpeedFactor 0 prevents movement
- [ ] Guard blocks exit
- [ ] Force through guard with skill check
- [ ] Grapple initiated successfully
- [ ] Grappled can't move
- [ ] Grappled can't dodge
- [ ] Break free from grapple
- [ ] Grapple power calculated correctly

---

### Phase 12: Message Rendering

**Goal:** All combat messages with JS override pattern.

**Depends on:** All previous combat phases

| File | Changes |
|------|---------|
| `game/messages.go` | Message rendering logic |

**Key behaviors:**
- Each combat event has a renderer object (weapon, armor, combatant)
- Check for `render*` callback on renderer
- If callback returns `{Message: "..."}`, use it
- Otherwise use Go default message
- Messages vary by observer perspective (1st/2nd/3rd person)

**Render callbacks:**
- `renderAttack`, `renderUnarmedAttack`, `renderMiss`
- `renderDodge`, `renderParry`, `renderBlock`
- `renderDamageDealt`, `renderDamageReceived`, `renderArmorSoak`
- `renderCrit`, `renderDeath`, `renderBodyPartDisabled`
- `renderStatusApplied`, `renderStatusTick`, `renderStatusExpired`

**Testing:**
- [ ] Default messages work for all events
- [ ] JS override replaces default message
- [ ] Observer perspective varies correctly

---

### Phase 13: JS Override Callbacks

**Goal:** before* callbacks for combat modification.

**Depends on:** Phase 12 (message rendering complete)

| File | Changes |
|------|---------|
| `game/combat.go` | Override hook points |

**Callbacks:**
- `beforeAttack` - can cancel attack (pacifism, resource cost)
- `beforeDamage` - can modify/cancel damage (shields, immunity)
- `beforeDeath` - can cancel death (deathward, last stand)
- `beforeStatusApply` - can cancel status (immunity, resistance)

**Testing:**
- [ ] beforeAttack can cancel attack
- [ ] beforeDamage can modify damage amount
- [ ] beforeDamage can cancel damage entirely
- [ ] beforeDeath can prevent death (deathward)
- [ ] beforeStatusApply can block status effect

---

### Phase 14: Polish & Integration

**Goal:** Wizard tools, look enhancement, comprehensive tests.

**Depends on:** All previous phases

| File | Changes |
|------|---------|
| `game/wizcommands.go` | Config management commands |
| `game/look.go` | Body info in look output |
| `integration_test/combat_test.go` | Comprehensive tests |

**Wizard commands:**
- `/weaponconfig`, `/rangedweaponconfig`, `/ammoconfig`
- `/armorconfig`, `/bodyconfig`, `/stanceconfig`
- `/statusconfig`, `/damagetype`, `/combatconfig`

**Look enhancement:**
```
A large humanoid figure stands before you.
Body: humanoid (head, torso, arms, legs)
Wielding: longsword (right arm)
Wearing: chainmail (torso)
```

**Integration test scenarios:**
- Full melee combat flow (attack/defend/damage/death)
- Multi-combatant battle with flanking
- Ranged combat with aiming and cover
- Status effects during combat
- Equipment degradation over prolonged fight
- Grappling encounter
- Movement and pursuit
- Body part targeting and disability
- JS override callbacks (deathward, damage reflection)
- Message rendering with custom messages
- Server restart preserves CombatTargets (auto-resume)

---

### Phase Summary

| Phase | Name | Key Deliverable |
|-------|------|-----------------|
| 1 | Foundation | All config types accessible via JS |
| 2 | ObjectDO Schema | Combat fields on objects |
| 3 | Status Effects | Complete effect system |
| 4 | Equipment | Wielding/wearing with validation |
| 5 | Resources | Lazy regeneration system |
| 6 | Basic Melee | Core combat loop |
| 7 | Body Parts | Targeting and body part health |
| 8 | Advanced Melee | Multi-attack, wounds, stances |
| 9 | Ranged | Guns, ammo, reload, jam |
| 10 | Aiming & Cover | Aim bonus, range, cover |
| 11 | Movement & Grappling | Tactical movement, grapple |
| 12 | Messages | JS override for all messages |
| 13 | JS Overrides | before* callbacks |
| 14 | Polish | Wizard commands, tests |

**Estimated complexity per phase:**
- Phases 1-2: Low (mostly data definitions)
- Phases 3-5: Medium (independent systems)
- Phases 6-8: High (core combat logic)
- Phases 9-11: Medium-High (extensions)
- Phases 12-14: Medium (polish)

---

## Integration Analysis

This section analyzes how each phase integrates with the existing JuiceMUD codebase.

### Phase 1: Foundation - Integration

**What exists:** ServerConfig in `structs/serverconfig.go` with SkillConfig pattern (Get/Set/Delete/CAS/Snapshot/Replace methods, internal mutex, JSON marshal/unmarshal).

**Integration approach:**
1. Define all config structs in new `structs/combat.go` file
2. Add config maps to `ServerConfig` struct (private fields with methods)
3. Update `serverConfigJSON` for serialization
4. Add JS callbacks in `game/processing.go` following `getSkillConfig`/`updateSkillConfig` pattern:
   - `get*Config(name)` - read from in-memory ServerConfig
   - `update*Config(name, old, new)` - CAS in-memory → persist to root object → revert on failure

**Risk assessment: LOW**
- Pure extension of existing pattern
- No changes to existing behavior
- ServerConfig pattern is well-tested

**Code locations:**
```
structs/combat.go      - New file, define config types
structs/serverconfig.go - Add maps and methods (copy SkillConfig pattern)
game/processing.go     - Add callbacks in addGlobalCallbacks()
```

---

### Phase 2: ObjectDO Schema - Integration

**What exists:** ObjectDO defined in `structs/schema.benc`, auto-generated to `structs/schema.go` via bencgen. Fields include Location, Content, Skills, Descriptions, Exits, etc.

**Integration approach:**
1. Add new fields to `structs/schema.benc`
2. Run `go generate ./structs` to regenerate schema.go
3. Update `PostUnmarshal()` to initialize new maps (Wielding, Wearing, BodyParts, CombatTargets, etc.)
4. Existing serialization handles new fields automatically

**Risk assessment: LOW**
- Schema extension is routine
- bencgen handles serialization
- PostUnmarshal pattern already exists for map initialization
- No behavioral changes

**Code locations:**
```
structs/schema.benc    - Add field definitions
structs/schema.go      - Auto-generated (go generate)
```

**Migration concern:** Existing objects will have zero/nil values for new fields. This is fine - combat stats start at 0, maps initialize via PostUnmarshal.

---

### Phase 3: Status Effects - Integration

**What exists:** Interval system in `game/processing.go` with setInterval/clearInterval, persistent storage, recovery on restart. Events emitted via `emit()` callback.

**Integration approach:**
1. StatusEffect stored in `ObjectDO.StatusEffects` slice
2. `GetStatusEffects()` method on Object does lazy cleanup (check ExpiresAt, remove expired)
3. `ApplyStatusEffect()` creates StatusEffect, schedules tick interval if TickInterval > 0
4. Tick handler uses existing interval system - callback checks expiry, emits statusTick or handles expiry
5. `RemoveStatusEffect()` clears from slice, calls `clearInterval()` if ticking

**How it fits with existing systems:**
- Tick intervals: Use existing `setInterval()` mechanism (min will be 1s, max 10 per object)
- Events: Use existing `emit()` for statusApplied/statusExpired/statusTick
- Lazy cleanup: Similar pattern to how Descriptions.Detect filters by challenges
- Storage: StatusEffects serialize with ObjectDO automatically

**Risk assessment: LOW-MEDIUM**
- Must handle interval cleanup when object deleted
- ReplacedBy chain needs careful implementation to avoid loops

**Code locations:**
```
structs/combat.go      - StatusEffect type (or structs/status.go)
structs/structs.go     - GetStatusEffects(), ApplyStatusEffect() methods on Object
game/processing.go     - JS callbacks, tick handler registration
```

**Note:** Minimum tick interval will be 1 second (reduced from current 5s limit). For sub-second effects, use lazy timestamp computation instead.

---

### Phase 4: Equipment - Integration

**What exists:** ObjectDO.Content tracks contained objects. Objects have Location field. Movement system handles relocating objects.

**Integration approach:**
1. Wielding/Wearing maps store objectID references (objects still in Content)
2. `equip()` validates:
   - Object exists in Content (must be carried)
   - Object has WeaponConfig/ArmorConfig (check SourcePath or object state)
   - Body part exists on wielder's BodyConfig
   - Slot compatibility (SlotType matches)
   - Multi-slot availability
   - Armor layering (Thickness vs Looseness)
3. Equipment objects use their own Health field for durability

**How it fits with existing systems:**
- Objects remain in Content when equipped (Wielding/Wearing are references)
- Object lookup via `storage.AccessObject()` already exists
- No movement needed - equipment is a "soft reference" to contained object

**Risk assessment: LOW-MEDIUM**
- Straightforward reference tracking
- Validation logic is new but self-contained
- Need to handle: object removed from Content while equipped (auto-unequip)

**Code locations:**
```
game/equipment.go      - New file: equip/unequip logic
game/processing.go     - JS callbacks
```

**Edge case:** If equipped item is moved out of Content (via moveObject), must auto-unequip. The Go code in `moveObject` that triggers the `transmitted` event should also check if the moved object was in the source's Equipment map and remove it.

---

### Phase 5: Resources - Integration

**What exists:** Timestamp-based lazy computation used in skill system (LastUsedAt, forgetting decay). EffectiveSkills() uses time-based calculations.

**Integration approach:**
1. Health/Stamina/Focus stored as current value + LastRegenAt timestamp
2. `getHealth()` computes: `current + elapsed * regenRate * skillMod`
3. Skill modifier uses EffectiveSkills(RegenSkills) with sigmoid mapping
4. Returns min(computed, max) and updates LastRegenAt

**How it fits with existing systems:**
- Identical pattern to skill forgetting/recharge calculations
- Uses EffectiveSkills() for skill-based regen rate
- No timers needed - pure lazy computation

**Risk assessment: LOW**
- Pattern well-established in skill system
- Self-contained calculation
- No concurrency concerns (computed on access with object lock held)

**Code locations:**
```
game/resources.go      - New file: regeneration logic
game/processing.go     - JS callbacks (getHealth, setHealth, etc.)
```

---

### Phase 6: Basic Melee - Integration

**What exists:** Event queue with goroutine workers. Events fire callbacks on objects. No persistent attack timers currently.

**Integration approach:**
1. `startCombat(targetID)` adds target to CombatTargets map, creates cancellable context, spawns goroutine
2. Goroutine: select on context.Done() or timer, check constraints on wake, resolve attack, repeat
3. Attack resolution uses EffectiveSkills() and Roll() for accuracy, defense, etc.
4. Damage applied via setHealth() (with regen timestamp update)
5. `stopCombat()` removes from CombatTargets, cancels context; goroutine wakes immediately and exits
6. Death emits event, JS handles respawn/loot

**Goroutine lifecycle with context:**
```go
func (g *Game) attackLoop(ctx context.Context, attackerID, targetID string) {
    for {
        interval := calculateAttackInterval(...)
        select {
        case <-ctx.Done():
            return  // Combat ended, exit immediately
        case <-time.After(interval):
            // Validate constraints before acting
            if !stillInCombat(attackerID, targetID) {
                return
            }
            resolveAttack(attackerID, targetID)
        }
    }
}
```

**How it fits with existing systems:**
- EffectiveSkills() and Roll() for all skill checks - leverages entire skill system
- Events (attackHit, death, etc.) use existing emit() mechanism
- CombatTargets persists in ObjectDO - survives restart
- Goroutines are ephemeral (combat timing lost on restart, but CombatTargets triggers resume)

**Risk assessment: MEDIUM-HIGH**
- Goroutine lifecycle management is new pattern
- Must handle: object deleted mid-combat, target moves, etc.
- Defense chain is complex (parry → dodge → block → armor)
- Goroutines must validate constraints when waking up (target still exists, still in combat, etc.)

**Code locations:**
```
game/combat.go         - New file: combat logic, attack resolution
game/processing.go     - JS callbacks (startCombat, stopCombat, etc.)
game/game.go           - Combat restart hook (check CombatTargets on object load?)
```

**Restart behavior:** Combat resumes through visibility checks:
1. **Movement triggers**: When an object moves to a room, it checks if any objects in CombatTargets are present → resumes combat
2. **Movement events**: When receiving a movement event, check if the arriving object is in CombatTargets → resumes combat
3. **Lazy first-event check**: On first event after server restart, check if object has non-empty CombatTargets and target is visible → resume combat

Implementation for lazy first-event:
```go
type Game struct {
    startupTime structs.Timestamp  // Set when server starts from ctx.Now()
}

// In event handler, before running JS:
if obj.LastEventTime < uint64(g.startupTime) && len(obj.CombatTargets) > 0 {
    // First event since restart, has combat state - check for visible targets
}
obj.LastEventTime = uint64(ctx.Now())
```

This avoids iterating all objects at startup. Dormant objects that never receive events won't resume combat, but that's acceptable since nothing is happening to them anyway.

**Locking strategy:** When resolving attack:
```go
structs.WithLock(func() error {
    // Modify attacker (skill updates) and defender (health)
    return nil
}, attacker, defender)
```

Note: Callers must validate objects are non-nil before calling `WithLock`. If cover can be destroyed mid-attack, check before locking.

---

### Phase 7: Body Parts - Integration

**What exists:** Random selection used in movement perception (weighted by challenges). Descriptions have per-description filtering.

**Integration approach:**
1. BodyConfig loaded from ServerConfig at combat time
2. Body part selection: weighted random by HitWeight (similar to challenge-based filtering)
3. Focus/Defend modify weights before selection
4. BodyParts map tracks per-part health
5. Armor lookup: `object.Wearing[hitBodyPartID]` → only that armor applies

**How it fits with existing systems:**
- Weighted selection is straightforward (no existing utility, but simple to implement)
- EffectiveSkills() and Roll() for Focus/Defend skill checks
- Per-body-part state is just a map in ObjectDO

**Risk assessment: MEDIUM**
- New selection algorithm but conceptually simple
- Body part initialization from BodyConfig needs careful handling
- Dual damage (body part + central) must be atomic

**Code locations:**
```
game/combat.go         - Body part selection, damage distribution
structs/structs.go     - BodyParts initialization helper
```

**Initialization:** When object gets BodyConfigID set, BodyParts map should be populated from BodyConfig.Parts with each part's MaxHealth. Could be:
- Lazy on first combat
- Explicit via JS callback `initializeBodyParts()`
- Automatic in setBodyConfigID setter

---

### Phase 8: Advanced Melee - Integration

**What exists:** All foundations from earlier phases. Status effects (Phase 3), equipment (Phase 4), body parts (Phase 7).

**Integration approach:**

**Multi-attack:**
- Iterate body parts with UnarmedDamage or Wielding entries
- Each spawns independent attack goroutine with own timing
- AmbidextrousSkills check determines off-hand penalty (scaled via AmbidextrousPenaltyRange)

**Wounds (Go-managed):**
- Bleeding: Go checks DamageTypeConfig.BleedingMult + body part BleedingDamageThresholds
- Severing: Go checks DamageTypeConfig.SeverMult + overkill vs SeverThreshold
- Drop equipment from severed part (moveObject to room)

**Other Status Effects via ApplyStatusEffects:**
- After damage, check weapon/ammo/body-part ApplyStatusEffects
- For each ApplyStatusEffect: Challenge.Check() for attacker
- On success, status effect applied to defender (poison, burning, etc.)
- For armor ApplyStatusEffects: Challenge.Check() for defender to apply to attacker

**Stances:**
- StanceConfig loaded, modifiers applied in attack resolution
- EffectiveSkills(StanceSkills) → sigmoid-scaled modifier effectiveness

**Flanking:**
- Count objects with defender in their CombatTargets
- Count objects with any attacker in their CombatTargets
- Simple ratio calculation, bonus capped

**Risk assessment: HIGH**
- Many interacting systems
- Multi-goroutine coordination
- Edge cases around severing (equipment drop, grip factor)
- Stealth/ambush adds perception system interaction

**Code locations:**
```
game/combat.go         - Extended attack resolution
game/equipment.go      - Drop equipment on sever
```

**Stealth integration:** Ambush detection uses existing Description challenge system. Attacker is "hidden" if observer fails all description challenges. On attack, clear description challenges to reveal attacker.

---

### Phase 9: Ranged - Integration

**What exists:** Basic combat from Phase 6. Equipment system from Phase 4.

**Integration approach:**
1. RangedWeaponConfig on equipped weapon determines ranged behavior
2. CurrentAmmo, LoadedAmmoType, Jammed stored on weapon object (not wielder)
3. Reload: check free wield slot, spawn goroutine for reload delay
4. Jam check: AvoidJamChallenge.Check() on each shot
5. Damage calculation: weapon.DamageTypes + ammo.DamageTypes

**How it fits with existing systems:**
- Uses same attack resolution pipeline, different damage source
- Equipment system already tracks weapon objects
- Skill checks (EffectiveSkills/Roll) work the same way

**Risk assessment: MEDIUM**
- Straightforward extension of melee
- Reload/unjam timers are simple goroutine sleeps
- Magazine state on weapon object is clean

**Code locations:**
```
game/ranged.go         - New file: ranged combat logic
game/processing.go     - JS callbacks (reload, unjam, setFireMode)
```

---

### Phase 10: Aiming & Cover - Integration

**What exists:** Timestamp-based lazy computation (Phase 5 pattern). Neighbourhood queries for adjacent rooms.

**Integration approach:**

**Aiming:**
- AimingAt, AimingSince on attacker object
- getAimBonus() computes elapsed * rate * skillMod (lazy)
- Aim broken by: damage received (hook in applyDamage), movement (hook in moveObject)

**Range:**
- MaxRange 0: target must be in same Location
- MaxRange 1: target can be in Location or any exit.Destination
- Use existing DeepNeighbourhood to find valid targets

**Cover:**
- InCoverBehind references cover object
- Cover lookup: `storage.AccessObject(InCoverBehind)`
- Apply CoverAbsorption to damage, CoverAccuracyPenalty to hit
- Cover takes damage (update cover object's Health)

**Risk assessment: MEDIUM**
- Aiming follows established lazy pattern
- Range check uses existing neighbourhood queries
- Cover damage requires locking cover object too

**Code locations:**
```
game/ranged.go         - Aiming, range checks, cover application
game/processing.go     - JS callbacks, aim break hooks
```

**Multi-object locking:** Cover damage requires locking attacker, defender, AND cover:
```go
structs.WithLock(func() error {
    // Resolve attack with cover
}, attacker, defender, cover)
```

---

### Phase 11: Movement & Grappling - Integration

**What exists:** Movement system in processing.go with moveObject(), emit movement events. Exit challenges for movement validation.

**Integration approach:**

**Movement in combat:**
- Check CombatTargets before movement
- If in combat: apply CombatMovementDodgePenalty, block attack/parry/block
- Movement uses goroutine sleep for delay (existing pattern)

**Exit guarding:**
- Check for objects in room with GuardingExit matching direction
- If found: contested check (guard rolls GuardSkills vs traveler's escape roll)
- Winner determines if movement proceeds

**Grappling:**
- GrappledBy/Grappling fields on objects
- Grapple power calculated from free body parts
- Grappled restrictions enforced in relevant functions (movement, dodge, targeting)

**How it fits with existing systems:**
- Movement hooks into existing moveObject() flow
- Exit guarding is pre-movement check (before exit challenges)
- Grappling state is just object fields, checked in combat/movement

**Risk assessment: MEDIUM-HIGH**
- Movement integration touches core system
- Grappling has many restriction points to enforce
- Must not break existing movement for non-combat objects

**Code locations:**
```
game/movement.go       - New file: combat movement, grappling
game/processing.go     - Hook into moveObject(), JS callbacks
```

**Movement integration point:** In moveObject callback:
```go
// Before movement validation
if obj.InCombat() {
    // Apply combat movement restrictions
}
if guardingObject := findGuard(location, direction); guardingObject != nil {
    // Resolve guard challenge
}
```

---

### Phase 12: Message Rendering - Integration

**What exists:** emit() sends events to objects. Object callbacks can return values. Current movement rendering uses renderMovement callback.

**Integration approach:**
1. Each combat event identifies "renderer" object (weapon, armor, combatant)
2. Emit render* event to renderer with combat data
3. If callback returns `{Message: "..."}`, use it
4. Otherwise use Go default message
5. Final message emitted to all observers via emitToLocation()

**How it fits with existing systems:**
- Follows renderMovement pattern exactly
- Callbacks return JSON, parsed for Message field
- Observer perspective passed in event data

**Risk assessment: LOW**
- Pattern already established
- Pure extension, no changes to existing behavior

**Code locations:**
```
game/messages.go       - New file: default messages, render dispatch
game/combat.go         - Call renderMessage() at appropriate points
```

---

### Phase 13: JS Overrides - Integration

**What exists:** Callbacks can return values that modify behavior. beforeDamage pattern similar to how renderMovement can customize output.

**Integration approach:**
1. Before critical actions, emit before* event to relevant object
2. If callback returns `{Cancel: true}`, abort the action
3. If callback returns modified values, use them
4. Proceed with (possibly modified) action

**How it fits with existing systems:**
- Same callback invocation pattern
- Return value inspection already done for render callbacks

**Risk assessment: LOW**
- Clean extension point
- Well-defined return value contract

**Code locations:**
```
game/combat.go         - Add before* hooks at action points
```

---

### Phase 14: Polish - Integration

**What exists:** Wizard commands in game/wizcommands.go. Look output in connection handling.

**Integration approach:**
1. Wizard commands: parse arguments, call update*Config()
2. Look enhancement: check BodyConfigID, format body part list
3. Integration tests: use SSH interface per project conventions

**Risk assessment: LOW**
- Straightforward additions
- Following existing patterns

**Code locations:**
```
game/wizcommands.go    - Config commands
game/connection.go     - Look enhancement (or game/look.go)
integration_test/      - Combat tests
```

---

## Integration Risk Summary

| Phase | Risk | Key Concerns |
|-------|------|--------------|
| 1 | Low | Pure pattern extension |
| 2 | Low | Schema generation is routine |
| 3 | Low-Medium | Interval cleanup on object delete, ReplacedBy chain loops |
| 4 | Low-Medium | Auto-unequip on item movement |
| 5 | Low | Established lazy computation pattern |
| 6 | Medium-High | Goroutine lifecycle, multi-object locking, restart behavior |
| 7 | Medium | Body part initialization, dual damage atomicity |
| 8 | High | Many interacting systems, edge cases |
| 9 | Medium | Extension of Phase 6 patterns |
| 10 | Medium | Multi-object locking for cover |
| 11 | Medium-High | Core movement system integration |
| 12 | Low | Established render pattern |
| 13 | Low | Clean extension points |
| 14 | Low | Polish work |

**Highest risk areas:**
1. **Phase 6 (Basic Melee)**: Goroutine management, restart behavior, locking
2. **Phase 8 (Advanced Melee)**: System interactions, severing complexity
3. **Phase 11 (Movement)**: Integration with core movement system

**Mitigation strategies:**
- Build and test each phase completely before moving on
- Phase 6 needs careful design review before implementation
- Consider simplifying severing mechanics if implementation proves difficult
- Movement integration should be minimal and well-isolated

---

## Edge Cases

1. **Target moves**: Continue if reachable, stop if not
2. **Equipment destroyed**: Weapon/armor at 0 health becomes non-functional but stays equipped
3. **Multiple attackers**: Each has independent combat cycle
4. **Self-attack**: Prevent
5. **Dead target**: Stop combat, emit event
6. **Server restart**: CombatTargets persists, so hostiles auto-attack on sight after restart (no timer recovery needed)
7. **No weapon equipped**: Use unarmed attacks from ALL body parts with UnarmedDamage (simultaneous multi-attack)
8. **No armor on hit body part**: No soak for that body part, full damage taken
9. **Damage type not in parry/block/armor map**: No defense against that type
10. **Body part disabled (health = 0)**: Cannot attack or defend with that body part
11. **All attacking body parts disabled**: Cannot attack unarmed; must equip weapon or flee
12. **Unarmed blocking**: Not allowed - blocking requires wielding something (weapon, shield, tool)
13. **Equipment swap mid-combat**: Allowed but takes time; can't attack/parry/block during swap, dodge with penalty
14. **Dual-wield**: Both equipped weapons attack; each has its own speed roll and attack cycle
15. **Out of ammo**: Can't fire; must reload
16. **Weapon jammed**: Can't fire; must clear jam (skill-based time)
17. **Target behind cover**: Apply cover accuracy penalty and absorption
18. **Cover destroyed**: When cover health = 0, no benefit; find new cover
19. **Shooting into adjacent room**: Only if MaxRange >= 1; apply RangePenalty
20. **Aim interrupted**: Taking damage, moving, or target leaving room clears AimingSince
21. **Moving while in combat**: Can't attack/parry/block; dodge with penalty
22. **Attacking while moving**: Delays movement completion
23. **Chase target escapes**: JS decides when to give up (chase range, timeout, etc.)
24. **SpeedFactor 0**: Movement completely prevented (stunned, rooted)

---

## Key Design Decisions

1. **Attack skills have no recharge** - Weapon speed skill check controls timing
2. **Comparative defense** - Attacker's hit score compared against each defense score (dodge > hit = dodged). Challenge results can be negative (failure) or positive (success) - when comparing, the higher value wins, so a "lesser failure" (-5) beats a "greater failure" (-20)
3. **Defense chain is sequential** - Parry -> Dodge -> Block -> Armor soak -> Body part multiplier
4. **Crit determined early** - Crit check happens after accuracy, before defense chain; multiplier applied at end if attack lands
5. **Parry redirects** (no damage), **Block absorbs** (weapon takes damage)
6. **Equipment degrades 1:1** - Blocking/absorbing damage costs equipment health 1:1; efficacy scales with health ratio
7. **Death just emits event** - JS handles respawn/loot/etc.
8. **Use goroutines with sleep** - Variable timing via skill checks, no persistence needed (CombatTargets handles restart)
9. **Use existing skill system** - Leverages EffectiveSkills(), Roll() infrastructure
10. **Status effects: lazy + tick** - All effects checked lazily on access; ticking effects also checked at each tick
11. **Status effects emit events** - `statusApplied`, `statusExpired`, `statusTick`
12. **Optional timeout = permanent** - Implants are just status effects with no expiry
13. **Body configs define targetable parts** - Each body plan has different hit zones
14. **Stances have skill challenges** - Players can improve at using stances
15. **All configs persist in ServerConfig** - Same pattern as SkillConfigs
16. **Look shows body plan** - Visible body parts when examining creatures
17. **Defense chain** - Parry, dodge, block are whole-attack; armor soak is per-damage-type
18. **Message rendering via JS override** - Equipment/combatants can customize all combat messages
19. **Observer-aware messages** - First/second/third person based on who's observing
20. **Body-part equipment slots** - Each body part can have multiple compatible slot types, but only one item equipped at a time
21. **Qualified slot names** - Equipment uses `{bodyPartID}.{slotType}` format for universal body support (humanoids, octopi, dragons)
22. **Slot-based weapons** - Weapons specify slot type and count needed; multi-slot weapons occupy multiple body parts
23. **Unarmed multi-attack** - When unarmed, ALL body parts with UnarmedDamage attack (each with own speed roll)
24. **Dual-wield multi-attack** - When wielding weapons in multiple body parts, all weapons attack independently
25. **Body part health** - Each body part has health; at 0 it's disabled and cannot attack or defend
26. **Dual health tracking** - Damage applies to both body part AND central health; body parts can be disabled without death
27. **Unarmed defense** - Parry possible with skill via UnarmedParrySkills; blocking requires wielding something
28. **Multi-attack defense** - Defender rolls defense for each incoming attack; skill recharge makes repeated defenses harder
29. **Ranged damage = weapon + ammo** - Both contribute; bows add draw strength, guns add nothing
30. **Simple range model** - MaxRange 0 (same room) or 1 (adjacent); no exit configuration needed
31. **Point blank = active melee** - PointBlankModifier applies when in melee combat with target
32. **Jam/unjam skill-based** - JamSkills and UnjamSkills, not random chance
33. **Aim bonus from time** - Computed lazily from AimingSince timestamp
34. **Cover on objects and bodies** - CoverAbsorption + CoverAccuracyPenalty; bodies provide cover via BodyConfig
35. **Cover degrades 1:1** - Cover takes damage equal to absorbed amount
36. **Can't cover behind enemies** - Must not be in mutual CombatTargets
37. **Movement in combat = no attack/parry/block** - Tactical tradeoff for fleeing
38. **Attacks delay movement** - Can't sprint and fight simultaneously
39. **Chase in JS** - NPCs implement chase behavior via event callbacks
40. **SpeedFactor on status effects** - Movement delay divided by SpeedFactor; 0 = immobile
41. **Sigmoid for unbounded inputs** - Attack speed, armor soak, stance modifiers use sigmoid to map unbounded challenge results to bounded ranges
42. **Grapple power multiplies effective skill** - Effective skill multiplied by grapple power before Roll (octopus with 4.0 power has 4x effective skill)
43. **Three timing patterns** - Intervals for persistent events (status ticks), goroutines for ephemeral timers (attacks), lazy timestamps for continuous values (regen, aim)
44. **Lazy resource regeneration** - Health/Stamina/Focus computed on access using elapsed time and skill-based rate
45. **Regen enable flags** - Objects can disable natural regeneration (robots don't heal)
46. **JS override callbacks** - beforeAttack, beforeDamage, beforeDeath, beforeStatusApply can cancel/modify combat events
47. **Parry causes status effect** - Successful parry can apply status to attacker with sigmoid-based probability
48. **Per-damage-type armor penetration** - Ammo specifies penetration fraction per damage type
49. **Ranged defense penalties** - Weapons have DodgePenalty, ParryPenalty, BlockPenalty (bows easier to defend than guns)
50. **Thrown weapons are ammo** - IsThrown flag means weapon is consumed on throw, no separate ammo
51. **Weapon switching penalties** - Can't attack/parry/block while switching; dodge with penalty
52. **Flanking bonus** - Outnumbered defenders suffer accuracy/defense penalties based on attacker ratio
53. **Focus resource** - Mental actions use Focus (spells, aimed shots); Stamina for physical (feint, disarm)
54. **Aim rate skill-based** - AimSkills modify aim bonus accumulation rate via sigmoid
55. **Body part targeting via HitWeight** - Random selection weighted by HitWeight; focus/defend modify weights
56. **Focus/Defend risk-reward** - Good rolls improve targeting; bad rolls cause accuracy/defense penalties
57. **Configurable tuning constants** - Sigmoid divisor, multiplier ranges, penalties grouped by subsystem in CombatConfig
