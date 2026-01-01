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

1. **Leverage existing systems**: Use SkillConfig pattern, Challenge.Check(), timeouts
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

    // Body part health: bodyPartID -> current health (only for objects with BodyConfigID)
    // At 0, body part is disabled and cannot attack or defend
    BodyPartHealth map[string]float64

    // Body and stance (reference global configs)
    BodyConfigID   string  // References BodyConfig (humanoid, quadruped, etc.)
    StanceConfigID string  // References StanceConfig (aggressive, defensive, etc.)

    // Combat state
    CombatTargets map[string]bool  // Objects this object is attacking
    CurrentTarget string           // Primary target object ID (for focus)
    FocusBodyPart string           // Body part being targeted (if any)

    // Status effects (lazily cleaned on access)
    StatusEffects []StatusEffect
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
}
```

### BodyConfig

Defines body structure - what parts can be targeted, their modifiers, equipment slots, and unarmed combat capabilities.

```go
type BodyConfig struct {
    ID          string  // "humanoid", "quadruped", "serpent", etc.
    Description string

    Parts       []BodyPart
    DefaultPart string  // Which part is targeted by default ("torso")
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

    // Durability (0 = indestructible)
    MaxHealth       float64  // Maximum weapon health
    DegradationRate float64  // Multiplier for damage taken when blocking (e.g., 0.1 = 10% of blocked damage)
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

    // Durability (0 = indestructible)
    MaxHealth       float64  // Maximum armor health
    DegradationRate float64  // Multiplier for damage taken (e.g., 0.05 = 5% of soaked damage)

    // Efficacy = baseReduction * armorChallengeResult * (currentHealth / maxHealth)
}
```

**Armor and body parts:** When a body part is hit, only armor equipped in that body part's slots provides protection. A helmet only protects the head, chestArmor only protects the torso, etc.

**Equipment health:** Armor objects use their own `Health` field (from `ObjectDO`) to track durability. When health reaches 0, the armor provides no protection. It remains equipped but non-functional until repaired.

### DamageTypeConfig

```go
type DamageTypeConfig struct {
    ID          string  // "physical", "fire", "ice", etc.
    Description string
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
    ArmorConfigs        map[string]ArmorConfig
    BodyConfigs         map[string]BodyConfig
    StanceConfigs       map[string]StanceConfig
    StatusEffectConfigs map[string]StatusEffectConfig
    DamageTypes         map[string]DamageTypeConfig
    CombatConfig        CombatConfig
}
```

Each config type has a corresponding in-memory store (like `SkillConfigs`) that is:
- Loaded from ServerConfig at startup
- Updated via wizard commands
- Persisted back to ServerConfig on change

---

## Combat Flow

### Attack Cycle

1. **Initiate Combat**: `startCombat(targetID)` in JS
2. **Schedule Attack**: Go calculates next attack time via weapon speed skill check
3. **Attack Resolution**: When timeout fires:
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
       - If using weapon: weapon takes degradation: `weaponDamage += blockedAmount * weapon.DegradationRate`
       - If unarmed: blocking body part takes damage instead (natural armor absorbs hits)
   - Damage types without block challenges or failed blocks continue to next step

7. **Armor Soak** (per damage type, body-part specific):
   - Find armor equipped in the **hit body part's** slots
   - If no armor on that body part, or armor health = 0, skip to next step
   - For each remaining (unparried, unblocked) damage type:
     - If `ArmorChallenges[damageType]` exists, apply armor skill challenge
     - Reduce by armor's `BaseReduction[damageType]`
     - Apply degradation: `actualReduction = baseReduction * challengeResult * (armorHealth / armorMaxHealth)`
     - Armor takes degradation: `armorDamage += soakedAmount * armor.DegradationRate`
   - Damage types not in armor's BaseReduction pass through fully

8. **Critical Multiplier**:
   - If `isCrit` (from step 3), apply `CritDamageMultiplier` to remaining damage

9. **Body Part Damage Multiplier**:
   - Apply `bodyPart.DamageMultiplier` to final damage

10. **Apply Damage** (to both body part and central health):
    - Reduce hit body part's health (`BodyPartHealth[bodyPartID]`)
    - If body part health <= 0, body part is **disabled** (emit `bodyPartDisabled` event)
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
- `scheduleAttack(attackerID, targetID)` - uses setTimeout
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
6. **Server restart**: Recover from CombatTargets, reschedule attacks
7. **No weapon equipped**: Use unarmed attacks from ALL body parts with UnarmedDamage (simultaneous multi-attack)
8. **No armor on hit body part**: No soak for that body part, full damage taken
9. **Damage type not in parry/block/armor map**: No defense against that type
10. **Body part disabled (health = 0)**: Cannot attack or defend with that body part
11. **All attacking body parts disabled**: Cannot attack unarmed; must equip weapon or flee
12. **Unarmed block without natural armor**: Body part takes damage from blocking (dragon scales absorb, human arms get hurt)
13. **Equipment swap mid-combat**: Allowed but takes time (delays next attack); JS can customize swap duration
14. **Dual-wield**: Both equipped weapons attack; each has its own speed roll and attack cycle

---

## Key Design Decisions

1. **Attack skills have no recharge** - Weapon speed skill check controls timing
2. **Comparative defense** - Attacker's hit score compared against each defense score (dodge > hit = dodged). Challenge results can be negative (failure) or positive (success) - when comparing, the higher value wins, so a "lesser failure" (-5) beats a "greater failure" (-20)
3. **Defense chain is sequential** - Dodge -> Parry -> Block -> Armor soak -> Body part multiplier
4. **Crit determined early** - Crit check happens after accuracy, before defense chain; multiplier applied at end if attack lands
5. **Parry redirects** (no damage), **Block absorbs** (weapon takes degradation damage)
6. **Equipment degrades** - Efficacy = base * (current/max health); broken equipment is useless
7. **Death just emits event** - JS handles respawn/loot/etc.
8. **Use timeouts, not intervals** - Variable timing based on skill checks
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
