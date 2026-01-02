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

1. **Leverage existing systems**: Use SkillConfig pattern, Challenge.Check()
2. **Go-heavy logic**: Combat calculations in Go, JS only for events/customization
3. **Wizard-configurable**: All weapons, armor, damage types defined via configs
4. **Equipment degradation**: Both armor and weapons have health that affects efficacy
5. **Message customization**: All combat messages support optional JS override with Go defaults

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
    Stamina   float64  // Resource for special moves (reserved for future use)
    MaxStamina float64

    // Equipment: qualified slot name -> equipped object ID
    // Format: "{bodyPartID}.{slotType}" - works for any body configuration
    // e.g., {"head.helmet": "obj123", "torso.chestArmor": "obj456", "rightArm.weapon": "obj789"}
    // Multi-slot weapons appear in multiple entries with same object ID:
    // e.g., {"rightArm.weapon": "greatsword1", "leftArm.weapon": "greatsword1"}
    Equipment map[string]string

    // Body part state: bodyPartID -> state (only for objects with BodyConfigID)
    BodyParts map[string]BodyPartState

    // Body and stance (reference global configs)
    BodyConfigID   string  // References BodyConfig (humanoid, quadruped, etc.)
    StanceConfigID string  // References StanceConfig (aggressive, defensive, etc.)

    // Combat state
    CombatTargets map[string]bool  // Objects this object is attacking
    CurrentTarget string           // Primary target object ID (for focus)
    FocusBodyPart string           // Body part being targeted (if any)

    // Status effects (lazily cleaned on access)
    StatusEffects []StatusEffect

    // Cover properties (default 0 = not useful as cover)
    CoverAbsorption      float64  // 0-1, damage absorbed when used as cover
    CoverAccuracyPenalty float64  // Accuracy penalty to hit someone behind this

    // Cover state (for combatants)
    InCoverBehind string  // Object ID providing cover (empty = no cover)

    // Ranged weapon state (for weapon objects)
    CurrentAmmo    int     // Rounds in magazine
    LoadedAmmoType string  // Which AmmoConfig currently loaded
    Jammed         bool    // Weapon is jammed

    // Aiming state (for combatants)
    AimingAt    string     // Target object ID being aimed at
    AimingSince time.Time  // When aiming started (zero = not aiming); bonus computed lazily
}
```

### StatusEffect

Effects that modify combat. Optional timeout allows permanent effects (implants). Optional interval for ticking effects (poison, regen).

```go
type StatusEffect struct {
    ID          string     // Unique ID for this effect instance
    ConfigID    string     // References StatusEffectConfig
    AppliedAt   time.Time  // When effect was applied
    ExpiresAt   time.Time  // When effect expires (zero = permanent, e.g., implants)

    // State for ticking effects
    LastTickAt  time.Time  // Last time the tick event was emitted
}
```

**Expiry behavior:**
- All status effects are checked lazily on access (e.g., `GetStatusEffects()`)
- Effects with tick intervals are ALSO checked at each tick
- When expired (by lazy check or tick), emit `statusExpired` and clear any interval
- When applied, emit `statusApplied`

### BodyPartState

Tracks the current state of each body part on an object.

```go
type BodyPartState struct {
    Health  float64  // Current health; at 0 = disabled
    Severed bool     // If true, body part has been permanently removed
}
```

**Severing mechanics:**
- A body part can be severed when damage would reduce health significantly below 0 (overkill)
- Only certain damage types can sever: `slashing`, `piercing` (not `bludgeoning`, `fire`, etc.)
- Threshold: If `finalDamage > bodyPart.MaxHealth * SeverThreshold` and health was already low
- When severed:
  - Emit `bodyPartSevered` event (different from `bodyPartDisabled`)
  - Any equipment in that body part's slots drops to the ground
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

    // Ticking effects
    TickInterval time.Duration  // 0 = no ticking; otherwise emits "statusTick" event

    // Whether this is removable (implants might not be)
    Permanent bool  // If true, no ExpiresAt is set when applied

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

### BodyConfig

Defines body structure - what parts can be targeted, their modifiers, equipment slots, and unarmed combat capabilities.

```go
type BodyConfig struct {
    ID          string  // "humanoid", "quadruped", "serpent", etc.
    Description string

    Parts       []BodyPart
    DefaultPart string  // Which part is targeted by default ("torso")

    // Cover properties (used when taking cover behind creatures with this body type)
    CoverAbsorption      float64  // 0-1, damage absorbed
    CoverAccuracyPenalty float64  // Accuracy penalty to hit someone behind this body
}

type BodyPart struct {
    ID               string   // "head", "torso", "leftArm", "tail", etc.
    Description      string   // For look output

    // Body part health (tracked in ObjectDO.BodyPartHealth)
    MaxHealth float64  // Maximum health for this body part; at 0 = disabled

    // Combat targeting modifiers
    HitModifier      float64  // Added to accuracy challenge (head = +20, harder to hit)
    DamageMultiplier float64  // Damage multiplier (head = 1.5x)
    CritBonus        float64  // Added to crit chance

    // Equipment slots this body part supports (can equip one item from compatible slots)
    // e.g., head: ["helmet"], leftArm: ["shield", "weapon"], rightArm: ["weapon"]
    // Empty array if body part has no equipment slots
    EquipSlots []string

    // Unarmed combat (if this body part can attack - e.g., arms can punch, legs can kick)
    // Empty UnarmedDamage = this body part cannot attack unarmed
    UnarmedDamage      map[string]float64  // e.g., {"physical": 5} for fist
    UnarmedSpeed       Challenges
    UnarmedAccuracy    Challenges
    UnarmedDamageBonus Challenges
    UnarmedDescription string              // e.g., "fist", "claw", "bite"

    // Unarmed defense (for blocking/parrying without a weapon)
    // Empty maps = cannot parry/block unarmed; requires natural armor (scales, thick hide) for effective blocking
    UnarmedParryChallenges map[string]Challenges  // damage type -> challenges (very hard for soft-skinned creatures)
    UnarmedBlockChallenges map[string]Challenges  // damage type -> challenges (requires natural armor)
}
```

**Example humanoid body config:**
```go
BodyConfig{
    ID: "humanoid",
    Parts: []BodyPart{
        {ID: "head", MaxHealth: 50, HitModifier: 20, DamageMultiplier: 1.5, CritBonus: 0.1,
         EquipSlots: []string{"helmet"}},
        {ID: "torso", MaxHealth: 100, HitModifier: 0, DamageMultiplier: 1.0,
         EquipSlots: []string{"chestArmor"}},
        {ID: "rightArm", MaxHealth: 60, HitModifier: 5, DamageMultiplier: 0.8,
         EquipSlots: []string{"weapon", "glove"},
         UnarmedDamage: map[string]float64{"physical": 5}, UnarmedDescription: "right fist"},
        {ID: "leftArm", MaxHealth: 60, HitModifier: 5, DamageMultiplier: 0.8,
         EquipSlots: []string{"shield", "weapon", "glove"},
         UnarmedDamage: map[string]float64{"physical": 5}, UnarmedDescription: "left fist"},
        {ID: "rightLeg", MaxHealth: 70, HitModifier: 10, DamageMultiplier: 0.7,
         EquipSlots: []string{"legArmor", "boots"},
         UnarmedDamage: map[string]float64{"physical": 8}, UnarmedDescription: "right kick"},
        {ID: "leftLeg", MaxHealth: 70, HitModifier: 10, DamageMultiplier: 0.7,
         EquipSlots: []string{"legArmor", "boots"},
         UnarmedDamage: map[string]float64{"physical": 8}, UnarmedDescription: "left kick"},
    },
    DefaultPart: "torso",
}
```

**Note:** Each body part can have multiple compatible slot types, but only ONE item can be equipped per body part. A leftArm with `["shield", "weapon", "glove"]` can hold a shield OR a weapon OR a glove, not all three.

**Example dragon body config (with natural armor for unarmed blocking):**
```go
BodyConfig{
    ID: "dragon",
    Parts: []BodyPart{
        {ID: "head", MaxHealth: 150, HitModifier: 25, DamageMultiplier: 2.0, CritBonus: 0.15,
         UnarmedDamage: map[string]float64{"physical": 30, "fire": 15}, UnarmedDescription: "bite",
         UnarmedBlockChallenges: map[string]Challenges{"physical": {{Skill: "dragonScale", Level: 5}}}},
        {ID: "leftForeclaw", MaxHealth: 100, HitModifier: 10, DamageMultiplier: 1.2,
         UnarmedDamage: map[string]float64{"physical": 20}, UnarmedDescription: "left claw",
         UnarmedParryChallenges: map[string]Challenges{"physical": {{Skill: "clawFighting", Level: 10}}},
         UnarmedBlockChallenges: map[string]Challenges{"physical": {{Skill: "dragonScale", Level: 8}}}},
        // ... more parts: rightForeclaw, body, wings, tail ...
    },
    DefaultPart: "body",
}
```

### StanceConfig

Stances with skill challenges - players can improve at using stances. Higher skill amplifies positive effects and reduces negative effects.

```go
type StanceConfig struct {
    ID          string  // "aggressive", "defensive", "evasive"
    Description string

    // Modifiers to combat (base values before skill adjustment)
    AccuracyModifier float64  // Added to accuracy challenges
    DamageModifier   float64  // Damage multiplier
    DodgeModifier    float64  // Added to dodge challenges
    ParryModifier    float64
    BlockModifier    float64

    // Skill challenges for this stance
    // Results improve positive modifiers and reduce negative modifiers
    // e.g., aggressive stance has +accuracy but -dodge; higher skill = more accuracy, less dodge penalty
    StanceChallenges Challenges  // e.g., [{Skill: "aggressiveStance", Level: 5}]
}
```

**Stance skill effect:** When applying stance modifiers, the `StanceChallenges.Check()` result scales them:
- Positive modifiers: `actualMod = baseMod * (1 + result/100)` (higher skill = bigger bonus)
- Negative modifiers: `actualMod = baseMod * (1 - result/100)` (higher skill = smaller penalty, clamped to 0)

### WeaponConfig

Uses existing `Challenges` type (`[]Challenge`) for skill checks. Each Challenge has a Skill name and Level (difficulty). Results are **summed** - same as exits/descriptions.

```go
type WeaponConfig struct {
    ID          string
    Description string

    // Equipment slot requirements
    SlotType      string  // Which slot type this weapon uses: "weapon", "shield", etc.
    SlotsRequired int     // How many of that slot type needed (1=one-handed, 2=two-handed)

    // Damage by type (e.g., {"physical": 10, "fire": 5} = 10 physical + 5 fire damage)
    DamageTypes  map[string]float64

    // Skill challenges (use existing Challenges type - results summed)
    // Each Challenge has {Skill, Level, Message}
    SpeedChallenges    Challenges  // e.g., [{Skill: "agility", Level: 10}, {Skill: "swordSpeed", Level: 5}]
    AccuracyChallenges Challenges  // For hit chance
    DamageChallenges   Challenges  // For damage bonus

    // Defense capabilities (per damage type - e.g., shield blocks physical well, not fire)
    // Empty map = cannot parry/block; presence of damage type key = can defend against that type
    ParryChallenges   map[string]Challenges   // damage type -> skill challenges for parry (redirect, no damage)
    BlockChallenges   map[string]Challenges   // damage type -> skill challenges for block (absorb, weapon takes damage)

    // Durability (0 = indestructible; blocking damage is 1:1)
    MaxHealth float64  // Maximum weapon health
}
```

**Equipment slot examples:**
| Weapon | SlotType | SlotsRequired | Notes |
|--------|----------|---------------|-------|
| Sword | "weapon" | 1 | One-handed |
| Greatsword | "weapon" | 2 | Two-handed (needs 2 weapon slots across body parts) |
| Shield | "shield" | 1 | Goes in shield slot |
| Tower Shield | "shield" | 2 | Massive shield needing both arms |

**Equipping multi-slot weapons:** When SlotsRequired > 1, the system finds that many body parts with the matching slot type and occupies all of them. A 4-armed creature could dual-wield greatswords.

**Equipment health:** Weapon objects use their own `Health` field (from `ObjectDO`) to track durability. When health reaches 0, the weapon is broken - it cannot deal damage, parry, or block. It remains equipped but non-functional until repaired.

**Repair:** Equipment repair is handled by JS - wizards can implement repair NPCs, repair spells, or other mechanisms as needed.

### ArmorConfig

```go
type ArmorConfig struct {
    ID          string
    Description string

    // Equipment slot - which slot type this armor occupies
    SlotType string  // e.g., "helmet", "chestArmor", "legArmor", "glove", "boots"

    // Protection per damage type: damage type -> base reduction ratio
    // e.g., {"physical": 0.5, "fire": 0.1} = 50% physical reduction, 10% fire reduction
    BaseReduction map[string]float64

    // Skill challenges per damage type (affects armor effectiveness)
    // e.g., {"physical": [{Skill: "heavyArmor", Level: 10}]}
    ArmorChallenges map[string]Challenges

    // Durability (0 = indestructible; absorbed damage is 1:1)
    MaxHealth float64  // Maximum armor health

    // Efficacy = baseReduction * armorChallengeResult * (currentHealth / maxHealth)
}
```

**Armor and body parts:** When a body part is hit, only armor equipped in that body part's slots provides protection. A helmet only protects the head, chestArmor only protects the torso, etc.

**Equipment health:** Armor objects use their own `Health` field (from `ObjectDO`) to track durability. When health reaches 0, the armor provides no protection. It remains equipped but non-functional until repaired.

### DamageTypeConfig

```go
type DamageTypeConfig struct {
    ID          string  // "slashing", "piercing", "bludgeoning", "fire", etc.
    Description string

    // Wound effects
    CanSever       bool  // Can this damage type sever body parts? (slashing, piercing = yes; bludgeoning, fire = no)
    CanCauseBleeding bool  // Does this damage type cause bleeding wounds? (slashing, piercing = yes)
    BleedingSeverity float64  // Multiplier for bleeding intensity (0 = none, 1 = normal, 2 = severe)
}
```

### CombatConfig

```go
type CombatConfig struct {
    MinAttackInterval time.Duration  // e.g., 1s
    MaxAttackInterval time.Duration  // e.g., 10s

    // Base challenges for dodge step of defense
    DodgeChallenges Challenges  // e.g., [{Skill: "agility", Level: 5}]

    // Critical hits
    BaseCritChance       float64  // e.g., 0.05 (5%)
    CritDamageMultiplier float64  // e.g., 2.0

    // Severing
    SeverThreshold float64  // Overkill multiplier to sever (e.g., 1.5 = 150% of max body part health as overkill)
    SeverCritBonus float64  // Bonus to sever chance on critical hits (e.g., 0.5 = +50% threshold reduction)

    // Bleeding
    BleedingThresholds []BleedingThreshold  // Damage thresholds that trigger bleeding
}

type BleedingThreshold struct {
    DamagePercent   float64  // % of max health as damage to trigger (e.g., 0.1 = 10% of max health)
    StatusEffectID  string   // Which bleeding status effect to apply (e.g., "bleeding_light")
}
```

---

## Config Persistence

All global configs are stored in the root object's ServerConfig (same pattern as SkillConfigs):

```go
type ServerConfig struct {
    Spawn struct {
        Container string
    }
    SkillConfigs        map[string]SkillConfig
    WeaponConfigs       map[string]WeaponConfig
    RangedWeaponConfigs map[string]RangedWeaponConfig
    AmmoConfigs         map[string]AmmoConfig
    ArmorConfigs        map[string]ArmorConfig
    BodyConfigs         map[string]BodyConfig
    StanceConfigs       map[string]StanceConfig
    StatusEffectConfigs map[string]StatusEffectConfig
    DamageTypes         map[string]DamageTypeConfig
    CombatConfig        CombatConfig
    MovementConfig      MovementConfig
}
```

Each config type has a corresponding in-memory store (like `SkillConfigs`) that is:
- Loaded from ServerConfig at startup
- Updated via wizard commands
- Persisted back to ServerConfig on change

---

## Combat Flow

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

   **Multi-attack balance:** While multi-attack seems powerful, unarmed damage is much lower than weapons, unarmed blocking is difficult without natural armor, and defenders benefit from skill recharge mechanics - repeated defenses against the same attack type become harder, but defenders still get a chance against each incoming attack.

2. **Accuracy Check**:
   - Attacker rolls `AccuracyChallenges.Check()`
   - Add body part's `HitModifier` (targeting head is harder)
   - Add stance's `AccuracyModifier`
   - Add status effect modifiers
   - -> `hitScore`

3. **Critical Check** (determined early, applied later if attack lands):
   - Roll against `BaseCritChance + bodyPart.CritBonus`
   - Store `isCrit` flag for later

4. **Dodge**:
   - Defender rolls `DodgeChallenges.Check()`
   - Add stance's `DodgeModifier`
   - Add status effect modifiers
   - -> if `dodgeScore > hitScore`, attack misses entirely

5. **Parry** (per damage type, weapon or unarmed):
   - Use weapon's `ParryChallenges` if weapon equipped and healthy
   - Otherwise use defender's body part `UnarmedParryChallenges` (if any - requires skill/natural ability)
   - For each damage type in attack:
     - If `ParryChallenges[damageType]` exists, defender rolls those challenges
     - Add stance's `ParryModifier`
     - If `parryScore > hitScore`, that damage type is parried (no damage for that type)
   - Damage types without parry challenges or failed parries continue to next step

6. **Block** (per damage type, weapon or unarmed):
   - Use weapon's `BlockChallenges` if weapon equipped and healthy
   - Otherwise use defender's body part `UnarmedBlockChallenges` (if any - requires natural armor)
   - For each remaining (unparried) damage type:
     - If `BlockChallenges[damageType]` exists, defender rolls those challenges
     - Add stance's `BlockModifier`
     - If `blockScore > hitScore`, that damage type is blocked:
       - Damage for that type is negated
       - If using weapon: weapon takes damage equal to blocked amount
       - If unarmed: blocking body part takes damage instead (natural armor absorbs hits)
   - Damage types without block challenges or failed blocks continue to next step

7. **Armor Soak** (per damage type, body-part specific):
   - Find armor equipped in the **hit body part's** slots
   - If no armor on that body part, or armor health = 0, skip to next step
   - For each remaining (unparried, unblocked) damage type:
     - If `ArmorChallenges[damageType]` exists, apply armor skill challenge
     - Calculate reduction: `reduction = baseReduction * challengeResult * (armorHealth / armorMaxHealth)`
     - Reduce damage by that amount
     - Armor takes damage equal to absorbed amount (1:1)
   - Damage types not in armor's BaseReduction pass through fully

8. **Critical Multiplier**:
   - If `isCrit` (from step 3), apply `CritDamageMultiplier` to remaining damage

9. **Body Part Damage Multiplier**:
   - Apply `bodyPart.DamageMultiplier` to final damage

10. **Apply Damage** (to both body part and central health):
    - Reduce hit body part's health (`BodyParts[bodyPartID].Health`)
    - **Severing check**: If damage type has `CanSever` AND body part health went below 0:
      - Calculate overkill: `overkill = abs(newHealth)`
      - If `overkill > bodyPart.MaxHealth * SeverThreshold` (reduced by `SeverCritBonus` on crit):
        - Set `BodyParts[bodyPartID].Severed = true`
        - Drop equipped items from that body part
        - Optionally create severed body part object
        - Emit `bodyPartSevered` event
      - Else: body part is **disabled** (emit `bodyPartDisabled` event)
    - **Bleeding check**: If damage type has `CanCauseBleeding`:
      - Calculate `damagePercent = finalDamage / defender.MaxHealth`
      - Apply highest matching `BleedingThreshold` status effect (scaled by `BleedingSeverity`)
    - Reduce defender's central `Health` by same amount
    - If central Health <= 0, emit `death` event

### Attack Timing

Uses existing `Challenges.Check()` - results are summed across all challenges:

```go
func calculateAttackInterval(attacker *Object, weapon *WeaponConfig, config *CombatConfig) time.Duration {
    // Use existing Challenges.Check() - sums results from all challenges
    speedResult := weapon.SpeedChallenges.Check(attacker, "")

    // Higher skill = shorter interval (faster attacks)
    // Clamp result to reasonable range
    normalized := math.Max(0, math.Min(1, (speedResult+100)/200))

    interval := config.MaxAttackInterval - time.Duration(
        float64(config.MaxAttackInterval-config.MinAttackInterval) * normalized,
    )
    return interval
}
```

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

## Wound System

Combat can inflict persistent wounds via status effects.

### Bleeding

Caused by damage types with `CanCauseBleeding: true`. Bleeding naturally **heals over time** (clotting).

**Example configs:**
```go
"bleeding_light":    {TickInterval: 10s, /* 1-2 dmg/tick, heals in ~2min */}
"bleeding_moderate": {TickInterval: 5s,  ChallengeModifiers: {"accuracy": -5}}
"bleeding_severe":   {TickInterval: 3s,  ChallengeModifiers: {"accuracy": -10, "dodge": -10}}
```

**Natural healing:** Bleeding downgrades over time: severe → moderate → light → healed. Medical treatment stops it immediately.

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
    SlotType      string  // "weapon"
    SlotsRequired int     // 1 = pistol, 2 = rifle/bow

    // Range (0 = same room only, 1 = can shoot into adjacent room)
    MaxRange     int      // Most weapons: 0; rifles: 1
    RangePenalty float64  // Accuracy penalty when shooting into adjacent room

    // Point blank modifier (applies when in active melee combat with target)
    // Shotguns/pistols are good up close (+), rifles are awkward (-)
    PointBlankModifier float64

    // Weapon's damage contribution (added to ammo damage)
    // For bows: represents draw strength. For guns: usually empty.
    DamageTypes      map[string]float64
    DamageChallenges Challenges

    // Skill challenges
    AccuracyChallenges Challenges
    FireRateChallenges Challenges
    MinFireInterval    time.Duration
    MaxFireInterval    time.Duration

    // Ammunition
    MagazineSize     int
    ReloadChallenges Challenges  // Skill reduces reload time
    BaseReloadTime   time.Duration
    MinReloadTime    time.Duration
    CompatibleAmmo   []string  // Which AmmoConfig IDs this weapon can use

    // Fire modes
    FireModes []FireModeConfig

    // Reliability (all skill-based)
    JamChallenges   Challenges  // Higher skill = less jamming
    UnjamChallenges Challenges  // Higher skill = faster unjam
    BaseUnjamTime   time.Duration
    MinUnjamTime    time.Duration

    // Durability (0 = indestructible)
    MaxHealth float64
}

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
    DamageTypes      map[string]float64  // e.g., {"piercing": 15}
    DamageChallenges Challenges

    // Armor interaction
    ArmorPenetration float64  // 0-1, reduces armor effectiveness

    // Wound effects
    CanCauseBleeding bool
    BleedingSeverity float64

    // Special effects
    StatusEffectID     string   // e.g., "burning" for incendiary
    StatusEffectChance float64
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

Aiming improves accuracy over time. Bonus computed lazily from `AimingSince`:

```go
func getAimBonus(shooter *Object, config *CombatConfig) float64 {
    if shooter.AimingSince.IsZero() {
        return 0
    }
    elapsed := time.Since(shooter.AimingSince)
    bonus := elapsed.Seconds() * config.AimBonusPerSecond
    return math.Min(bonus, config.MaxAimBonus)
}
```

**Aim is broken by:** taking damage, moving, target moving rooms, being grappled, shooting.

### Jam Mechanics

Jamming is skill-based, not random chance:

```go
// Jam check per shot (modified by weapon health)
healthPenalty := (1 - weapon.Health/weapon.MaxHealth) * 50
jamResult := weapon.JamChallenges.Check(shooter) - healthPenalty
if jamResult < 0 {
    weapon.Jammed = true
}

// Unjam time based on skill
unjamResult := weapon.UnjamChallenges.Check(shooter)
unjamTime := BaseUnjamTime - (unjamResult * factor)  // Clamp to MinUnjamTime
```

### Ranged Defense Chain

1. **Range Check** - Target within MaxRange rooms?
2. **Accuracy Roll** - AccuracyChallenges.Check()
   - Subtract: RangePenalty (if shooting into adjacent room)
   - Add/Subtract: PointBlankModifier (if in active melee with target)
   - Add/Subtract: FireMode.AccuracyModifier
   - Add: Aim bonus
   - Subtract: Target's cover penalty
   - Add: Body part HitModifier
3. **Dodge** - Defender rolls with RangedDodgePenalty (bullets are fast)
4. **Cover** - If behind cover, apply absorption and accuracy penalty
5. **Block** - Only with shields rated for ranged (rare)
6. **Armor Soak** - Normal armor chain (ArmorPenetration reduces effectiveness)
7. **Apply Damage** - Normal (body part, bleeding, severing)

### CombatConfig Additions for Ranged

```go
type CombatConfig struct {
    // ... existing fields ...

    // Ranged combat
    RangedDodgePenalty float64  // Penalty to dodge ranged attacks (e.g., -30)
    AimBonusPerSecond  float64  // How much aim bonus per second (e.g., +10)
    MaxAimBonus        float64  // Cap on aim bonus (e.g., +50)
}
```

### Example Weapons

| Weapon | Slots | MaxRange | PointBlank | Magazine | Damage |
|--------|-------|----------|------------|----------|--------|
| Pistol | 1 | 0 | 0 | 12 | (from ammo) |
| Shotgun | 2 | 0 | +25 | 6 | (from ammo) |
| Longbow | 2 | 1 | -30 | 1 | piercing: 8 |
| Crossbow | 2 | 1 | -15 | 1 | piercing: 12 |
| Rifle | 2 | 1 | -15 | 30 | (from ammo) |

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
effectiveAbsorption := cover.CoverAbsorption * (cover.Health / cover.MaxHealth)
effectivePenalty := cover.CoverAccuracyPenalty * (cover.Health / cover.MaxHealth)
```

**Damage to cover:** Cover takes damage equal to absorbed amount (1:1). When cover health = 0, it provides no benefit.

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

Add perception challenges to all descriptions:
```go
Descriptions: []Description{
    {Content: "A figure lurks in shadows.",
     Challenges: []Challenge{{Skill: "perception", Level: 15}}},
}
```

If observer fails all challenges, they don't see the object (existing `look`/movement rendering handles this).

### Ambush

When hidden attacker initiates combat against target who can't see them:
- First attack ignores dodge
- Accuracy bonus (configurable)
- Auto-crit (configurable)

**Attacking removes stealth challenges** from descriptions, making attacker visible to all.

Being seen by a specific observer (they pass perception) only reveals you **to that observer** - challenges remain for others.

---

## Grappling System

Close-quarters combat for holds, throws, and restraint. Go handles enforcement; JS customizes messages.

### BodyPart Additions

```go
type BodyPart struct {
    // ... existing fields ...

    GrappleEffectiveness float64 // Contribution to grapple power (0 = can't grapple)
}
```

**Typical totals:** Humanoid = 1.0 (arms 0.4 each, legs 0.1 each), Giant octopus = 4.0 (8 tentacles at 0.5 each)

### Grapple Power Calculation

Total grapple power = sum of `GrappleEffectiveness` for all body parts where:
- `GrappleEffectiveness > 0`
- Body part is not disabled (health > 0)
- Body part is **not wielding a weapon** (no item in "weapon" slot for that body part)

**Skill check:** Each challenge's Level is **multiplied by grapple power** before the Check. A humanoid (1.0 power) uses normal skill levels; an octopus (4.0 power) effectively has 4× the skill levels.

**Examples (humanoid: 2 arms @ 0.4, 2 legs @ 0.1 = 1.0 total):**
- Unarmed: 1.0 grapple power (full skill)
- One-handed sword: 0.5 power (one arm + legs)
- Sword + shield: 0.2 power (only legs)
- Two-handed sword: 0.2 power (only legs)

**Note:** Shields are WeaponConfig with `SlotType: "weapon"`, so wielding a shield occupies that limb for grappling. Armor (ArmorConfig) does NOT occupy grappling limbs.

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
| `grapple` | Initiate grapple | GrappleChallenges × power vs target |
| `hold` | Maintain grapple, prevent escape | GrappleChallenges × power |
| `choke` | Deal damage over time while holding | GrappleChallenges + damage |
| `throw` | Release + knockdown + damage | GrappleChallenges × power |
| `break` | Escape from being grappled | GrappleChallenges × power vs grappler |
| `reverse` | Escape AND become the grappler | GrappleChallenges × power (harder) |
| `release` | Voluntarily release target | Automatic |

### CombatConfig Additions

```go
type CombatConfig struct {
    // ... existing fields ...

    // Grappling
    GrappleChallenges Challenges  // Skill challenges for grapple checks (levels × power)
    GrappleBreakBonus float64     // Bonus to break attempts (defender advantage)

    // Ambush
    AmbushAccuracyBonus float64  // Accuracy bonus when attacking from stealth
    AmbushAutoCrit      bool     // First attack from stealth auto-crits
}
```

---

## Movement in Combat

Moving while fighting has tradeoffs. No special "flee" state - just movement with combat penalties.

### Core Rules

**While moving in combat:**
- Cannot attack (busy moving)
- Cannot parry or block (not defending position)
- Dodge still works but with CombatMovementDodgePenalty
- Movement speed determined by MovementChallenges

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
    MovementChallenges  Challenges     // Higher skill = less delay
    BaseMovementDelay   time.Duration  // e.g., 2s
    MinMovementDelay    time.Duration  // e.g., 0.5s

    // Combat movement
    CombatMovementDodgePenalty float64  // Penalty to dodge while moving in combat

    // Exit guarding
    GuardChallenges Challenges  // Skill challenges for guard vs force-through
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
3. If guarded: both sides roll `GuardChallenges`
4. If challenger wins: movement proceeds (guard may get free attack)
5. If guard wins: movement blocked, optional message

---

## Implementation Phases

### Phase 1: Data Model & Config Stores

| File | Changes |
|------|---------|
| `structs/schema.go` | Add to ObjectDO: Health, MaxHealth, Stamina, MaxStamina, Equipment (map), BodyPartHealth (map), BodyConfigID, StanceConfigID, CombatTargets, StatusEffects |
| `structs/combat.go` | New file: All config types and their stores |
| `structs/status.go` | New file: StatusEffect, StatusEffectConfig, lazy cleanup logic |
| `game/game.go` | Expand ServerConfig, load all configs at startup |

Config stores to create (following SkillConfigStore pattern):
- WeaponConfigStore
- ArmorConfigStore
- BodyConfigStore
- StanceConfigStore
- StatusEffectConfigStore
- DamageTypeStore

### Phase 2: Status Effect System

| File | Changes |
|------|---------|
| `structs/status.go` | StatusEffect access with lazy expiry cleanup |
| `game/jscallbacks.go` | Status effect JS functions |

Key behaviors:
- `GetStatusEffects()` lazily removes expired, emits `statusExpired` for each
- `ApplyStatusEffect(configID, duration)` emits `statusApplied`, schedules tick interval if needed
- `RemoveStatusEffect(id)` emits `statusExpired`, clears any tick interval
- **Tick interval handler**: On each tick, first check if effect has expired
  - If expired: remove status effect, clear interval, emit `statusExpired`
  - If not expired: emit `statusTick`, reschedule next tick
- **Lazy + tick**: All effects checked lazily on access; ticking effects also checked at each tick

### Phase 3: Combat Core

| File | Changes |
|------|---------|
| `game/combat.go` | Combat logic, attack resolution, defense chain, message rendering |

Key functions:
- `startCombat(attackerID, targetID)`
- `stopCombat(attackerID, targetID)`
- `scheduleAttack(attackerID, targetID)` - spawns goroutine with sleep
- `resolveAttack(attacker, target, bodyPart)` - full defense chain with modifiers
- `calculateModifiers(object)` - sum stance + status effect modifiers
- `applyDamage(target, amount, types, bodyPart)`
- `renderCombatMessage(eventType, renderer, observer, data)` - JS override pattern

### Phase 4: JS API

| File | Changes |
|------|---------|
| `game/jscallbacks.go` | All combat JS functions |

**Combat functions:**
- `startCombat(targetID)` / `stopCombat(targetID)` / `stopAllCombat()`
- `setCurrentTarget(targetID)` / `getCurrentTarget()`
- `setFocusBodyPart(partName)` / `getFocusBodyPart()` / `clearFocusBodyPart()`

**Stats:**
- `getHealth()` / `setHealth(n)` / `getMaxHealth()` / `setMaxHealth(n)`
- `getStamina()` / `setStamina(n)` / `getMaxStamina()` / `setMaxStamina(n)`

**Equipment:**
- `equip(objectID, slotName)` - equip item to a specific slot (validates slot compatibility)
- `unequip(slotName)` - unequip item from a slot
- `getEquipped(slotName)` - get object ID equipped in a slot (empty string if none)
- `getEquipment()` - get all equipped items as `{slotName: objectID, ...}`
- `getEquipmentHealth(slotName)` - get health of item in slot (for degradation checks)

**Stance:**
- `setStance(stanceConfigID)` / `getStance()`

**Status Effects:**
- `applyStatusEffect(configID, duration)` / `removeStatusEffect(id)`
- `getStatusEffects()` / `hasStatusEffect(configID)`

**JS events emitted:**
- Combat: `attackHit`, `attackMissed`, `parried`, `blocked`, `damaged`, `death`, `criticalHit`, `bodyPartDisabled`
- Status: `statusApplied`, `statusExpired`, `statusTick`
- Render (for message customization): `renderAttack`, `renderUnarmedAttack`, `renderMiss`, `renderDodge`, `renderParry`, `renderBlock`, `renderDamageDealt`, `renderDamageReceived`, `renderArmorSoak`, `renderCrit`, `renderDeath`, `renderBodyPartDisabled`, `renderStatusApplied`, `renderStatusTick`, `renderStatusExpired`

### Phase 5: Look Output Enhancement

| File | Changes |
|------|---------|
| `game/look.go` or equivalent | Add body plan info to look output |

When looking at object with BodyConfigID:
```
A large humanoid figure stands before you.
Body: humanoid (head, torso, arms, legs)
```

### Phase 6: Wizard Commands

| File | Changes |
|------|---------|
| `game/wizcommands.go` | Config management commands |

Commands:
- `/weaponconfig [id] [field] [value]`
- `/armorconfig [id] [field] [value]`
- `/bodyconfig [id] [field] [value]`
- `/stanceconfig [id] [field] [value]`
- `/statusconfig [id] [field] [value]`
- `/damagetype [id] [description]`
- `/combatconfig [field] [value]`

### Phase 7: Integration & Tests

| File | Changes |
|------|---------|
| `integration_test/combat_test.go` | New file: combat integration tests |

Test scenarios:
- Basic attack/defend cycle
- Body part targeting with modifiers
- Armor only protects hit body part
- Unarmed multi-attack (all body parts attack simultaneously)
- Unarmed parry/block (with and without natural armor)
- Multi-slot weapon equipping (two-handed)
- Body part health tracking and disabling
- Disabled body part cannot attack/defend
- Damage flows to both body part and central health
- Stance changes affecting combat
- Status effects applying and expiring
- Status effect ticking
- Equipment degradation
- Broken equipment behavior
- Critical hits
- Message rendering with JS override
- Message rendering with Go defaults

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
12. **Unarmed block without natural armor**: Body part takes damage from blocking (dragon scales absorb, human arms get hurt)
13. **Equipment swap mid-combat**: Allowed but takes time (delays next attack); JS can customize swap duration
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
3. **Defense chain is sequential** - Dodge -> Parry -> Block -> Armor soak -> Body part multiplier
4. **Crit determined early** - Crit check happens after accuracy, before defense chain; multiplier applied at end if attack lands
5. **Parry redirects** (no damage), **Block absorbs** (weapon takes damage)
6. **Equipment degrades 1:1** - Blocking/absorbing damage costs equipment health 1:1; efficacy scales with health ratio
7. **Death just emits event** - JS handles respawn/loot/etc.
8. **Use goroutines with sleep** - Variable timing via skill checks, no persistence needed (CombatTargets handles restart)
9. **Use existing Challenges type** - Leverages existing skill system infrastructure
10. **Status effects: lazy + tick** - All effects checked lazily on access; ticking effects also checked at each tick
11. **Status effects emit events** - `statusApplied`, `statusExpired`, `statusTick`
12. **Optional timeout = permanent** - Implants are just status effects with no expiry
13. **Body configs define targetable parts** - Each body plan has different hit zones
14. **Stances have skill challenges** - Players can improve at using stances
15. **All configs persist in ServerConfig** - Same pattern as SkillConfigs
16. **Look shows body plan** - Visible body parts when examining creatures
17. **Per-damage-type defense** - Parry, block, and armor challenges vary by damage type
18. **Message rendering via JS override** - Equipment/combatants can customize all combat messages
19. **Observer-aware messages** - First/second/third person based on who's observing
20. **Body-part equipment slots** - Each body part can have multiple compatible slot types, but only one item equipped at a time
21. **Qualified slot names** - Equipment uses `{bodyPartID}.{slotType}` format for universal body support (humanoids, octopi, dragons)
22. **Slot-based weapons** - Weapons specify slot type and count needed; multi-slot weapons occupy multiple body parts
23. **Unarmed multi-attack** - When unarmed, ALL body parts with UnarmedDamage attack (each with own speed roll)
24. **Dual-wield multi-attack** - When wielding weapons in multiple body parts, all weapons attack independently
25. **Body part health** - Each body part has health; at 0 it's disabled and cannot attack or defend
26. **Dual health tracking** - Damage applies to both body part AND central health; body parts can be disabled without death
27. **Unarmed defense** - Parry possible with skill; block requires natural armor (scales, thick hide) or body part takes damage
28. **Multi-attack defense** - Defender rolls defense for each incoming attack; skill recharge makes repeated defenses harder
29. **Ranged damage = weapon + ammo** - Both contribute; bows add draw strength, guns add nothing
30. **Simple range model** - MaxRange 0 (same room) or 1 (adjacent); no exit configuration needed
31. **Point blank = active melee** - PointBlankModifier applies when in melee combat with target
32. **Jam/unjam skill-based** - JamChallenges and UnjamChallenges, not random chance
33. **Aim bonus from time** - Computed lazily from AimingSince timestamp
34. **Cover on objects and bodies** - CoverAbsorption + CoverAccuracyPenalty; bodies provide cover via BodyConfig
35. **Cover degrades 1:1** - Cover takes damage equal to absorbed amount
36. **Can't cover behind enemies** - Must not be in mutual CombatTargets
37. **Movement in combat = no attack/parry/block** - Tactical tradeoff for fleeing
38. **Attacks delay movement** - Can't sprint and fight simultaneously
39. **Chase in JS** - NPCs implement chase behavior via event callbacks
40. **SpeedFactor on status effects** - Movement delay divided by SpeedFactor; 0 = immobile
