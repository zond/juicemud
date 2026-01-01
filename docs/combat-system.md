# Combat System for JuiceMUD

## Overview

A flexible, wizard-configurable combat system that:
- Uses the existing skill system for all calculations
- Has variable attack timing based on weapon speed skill checks
- Implements N-to-M combat (can fight multiple targets who may not fight back)
- Keeps most logic in Go to minimize JS execution
- Is fully wizard-configurable (skills, weapons, armor, damage types)

## Design Principles

1. **Leverage existing systems**: Use SkillConfig pattern, Challenge.Check(), timeouts
2. **Go-heavy logic**: Combat calculations in Go, JS only for events/customization
3. **Wizard-configurable**: All weapons, armor, damage types defined via configs
4. **Equipment degradation**: Both armor and weapons have health that affects efficacy

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
    Stamina   float64  // Resource for special moves
    MaxStamina float64

    // Equipment references (empty string = none equipped)
    WeaponID string  // ID of currently equipped weapon object
    ArmorID  string  // ID of currently equipped armor object

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

Access to StatusEffects lazily removes expired ones. When removed (expired or explicitly), emits `statusExpired` event. When applied, emits `statusApplied` event.

### StatusEffectConfig

```go
type StatusEffectConfig struct {
    Name        string
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

Defines body structure - what parts can be targeted, their modifiers.

```go
type BodyConfig struct {
    Name        string  // "humanoid", "quadruped", "serpent", etc.
    Description string

    Parts []BodyPart
    DefaultPart string  // Which part is targeted by default ("torso")
}

type BodyPart struct {
    Name             string   // "head", "torso", "leftArm", "tail", etc.
    HitModifier      float64  // Added to accuracy challenge (head = +20, harder to hit)
    DamageMultiplier float64  // Damage multiplier (head = 1.5x)
    CritBonus        float64  // Added to crit chance
    Description      string   // For look output
}
```

### StanceConfig

Stances with skill challenges - players can improve at using stances.

```go
type StanceConfig struct {
    Name        string  // "aggressive", "defensive", "evasive"
    Description string

    // Modifiers to combat
    AccuracyModifier float64  // Added to accuracy challenges
    DamageModifier   float64  // Damage multiplier
    DodgeModifier    float64  // Added to dodge challenges
    ParryModifier    float64
    BlockModifier    float64

    // Skill challenges for this stance (can improve at stances)
    StanceChallenges Challenges  // e.g., [{Skill: "aggressiveStance", Level: 5}]
}
```

### WeaponConfig

Uses existing `Challenges` type (`[]Challenge`) for skill checks. Each Challenge has a Skill name and Level (difficulty). Results are **summed** - same as exits/descriptions.

```go
type WeaponConfig struct {
    Name        string
    Description string

    // Damage by type (e.g., {"physical": 10, "fire": 5} = 10 physical + 5 fire damage)
    DamageTypes  map[string]float64

    // Skill challenges (use existing Challenges type - results summed)
    // Each Challenge has {Skill, Level, Message}
    SpeedChallenges    Challenges  // e.g., [{Skill: "agility", Level: 10}, {Skill: "swordSpeed", Level: 5}]
    AccuracyChallenges Challenges  // For hit chance
    DamageChallenges   Challenges  // For damage bonus

    // Defense capabilities
    CanParry          bool        // Can redirect attacks (no damage)
    CanBlock          bool        // Can absorb damage (weapon takes damage)
    ParryChallenges   Challenges  // Skill challenges for parry
    BlockChallenges   Challenges  // Skill challenges for block

    // Durability
    MaxHealth float64  // Weapon durability (0 = indestructible)
}
```

### ArmorConfig

```go
type ArmorConfig struct {
    Name        string
    Description string

    // Protection: damage type -> base reduction ratio
    BaseReduction map[string]float64

    // Skill challenges (use existing Challenges type)
    ArmorChallenges Challenges  // Skills affecting armor effectiveness

    // Durability
    MaxHealth float64

    // Efficacy = baseReduction * (currentHealth / maxHealth)
}
```

### DamageTypeConfig

```go
type DamageTypeConfig struct {
    Name        string  // "physical", "fire", "ice", etc.
    Description string
}
```

### CombatConfig

```go
type CombatConfig struct {
    MinAttackInterval time.Duration  // e.g., 1s
    MaxAttackInterval time.Duration  // e.g., 10s

    // Base challenges for defense (added to weapon-specific challenges)
    DodgeChallenges Challenges  // e.g., [{Skill: "agility", Level: 5}]

    // Critical hits
    BaseCritChance     float64  // e.g., 0.05 (5%)
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
    SkillConfigs       map[string]SkillConfig
    WeaponConfigs      map[string]WeaponConfig
    ArmorConfigs       map[string]ArmorConfig
    BodyConfigs        map[string]BodyConfig
    StanceConfigs      map[string]StanceConfig
    StatusEffectConfigs map[string]StatusEffectConfig
    DamageTypes        map[string]DamageTypeConfig
    CombatConfig       CombatConfig
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
   - Roll accuracy (hit check)
   - If hit, run defense chain
   - Apply damage
   - Schedule next attack
4. **End Combat**: `stopCombat(targetID)` or incapacitation

### Defense Chain (Comparative)

Attacker's **to-hit result** is compared against each defense result. Stance and status effect modifiers are applied.

1. **Accuracy Check**:
   - Attacker rolls `AccuracyChallenges.Check()`
   - Add body part's `HitModifier` (targeting head is harder)
   - Add stance's `AccuracyModifier`
   - Add status effect modifiers
   - -> `hitScore`

2. **Dodge**:
   - Defender rolls `DodgeChallenges.Check()`
   - Add stance's `DodgeModifier`
   - Add status effect modifiers
   - -> if `dodgeScore > hitScore`, attack misses entirely

3. **Parry** (if weapon.CanParry):
   - Defender rolls `ParryChallenges.Check()`
   - Add stance's `ParryModifier`
   - -> if `parryScore > hitScore`, redirects, no damage

4. **Block** (if weapon.CanBlock):
   - Defender rolls `BlockChallenges.Check()`
   - Add stance's `BlockModifier`
   - -> if `blockScore > hitScore`, weapon absorbs damage

5. **Critical Check**:
   - Roll against `BaseCritChance + bodyPart.CritBonus`
   - If crit, apply `CritDamageMultiplier`

6. **Armor Soak**:
   - Reduce damage by armor's reduction for each damage type
   - Apply degradation: `actualReduction = baseReduction * (armorHealth / armorMaxHealth)`

7. **Body Part Damage Multiplier**:
   - Apply `bodyPart.DamageMultiplier` to final damage

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

## Implementation Phases

### Phase 1: Data Model & Config Stores

| File | Changes |
|------|---------|
| `structs/schema.go` | Add to ObjectDO: Health, MaxHealth, Stamina, MaxStamina, WeaponID, ArmorID, BodyConfigID, StanceConfigID, CombatTargets, StatusEffects |
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

### Phase 3: Combat Core

| File | Changes |
|------|---------|
| `game/combat.go` | Combat logic, attack resolution, defense chain |

Key functions:
- `startCombat(attackerID, targetID)`
- `stopCombat(attackerID, targetID)`
- `scheduleAttack(attackerID, targetID)` - uses setTimeout
- `resolveAttack(attacker, target, bodyPart)` - full defense chain with modifiers
- `calculateModifiers(object)` - sum stance + status effect modifiers
- `applyDamage(target, amount, types, bodyPart)`

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
- `equipWeapon(objectID)` / `equipArmor(objectID)`
- `unequipWeapon()` / `unequipArmor()`

**Stance:**
- `setStance(stanceConfigID)` / `getStance()`

**Status Effects:**
- `applyStatusEffect(configID, duration)` / `removeStatusEffect(id)`
- `getStatusEffects()` / `hasStatusEffect(configID)`

**JS events emitted:**
- Combat: `attackHit`, `attackMissed`, `parried`, `blocked`, `damaged`, `death`, `criticalHit`
- Status: `statusApplied`, `statusExpired`, `statusTick`

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
- `/weaponconfig [name] [field] [value]`
- `/armorconfig [name] [field] [value]`
- `/bodyconfig [name] [field] [value]`
- `/stanceconfig [name] [field] [value]`
- `/statusconfig [name] [field] [value]`
- `/damagetype [name] [description]`
- `/combatconfig [field] [value]`

### Phase 7: Integration & Tests

| File | Changes |
|------|---------|
| `integration_test/combat_test.go` | New file: combat integration tests |

Test scenarios:
- Basic attack/defend cycle
- Body part targeting with modifiers
- Stance changes affecting combat
- Status effects applying and expiring
- Status effect ticking
- Equipment degradation
- Critical hits

---

## Edge Cases

1. **Target moves**: Continue if reachable, stop if not
2. **Equipment destroyed**: Auto-unequip, continue unarmed/unarmored
3. **Multiple attackers**: Each has independent combat cycle
4. **Self-attack**: Prevent
5. **Dead target**: Stop combat, emit event
6. **Server restart**: Recover from CombatTargets, reschedule attacks

---

## Key Design Decisions

1. **Attack skills have no recharge** - Weapon speed skill check controls timing
2. **Comparative defense** - Attacker's hit score compared against each defense score (dodge > hit = dodged)
3. **Defense chain is sequential** - Dodge -> Parry -> Block -> Armor soak -> Crit check -> Body part multiplier
4. **Parry redirects** (no damage), **Block absorbs** (weapon takes damage)
5. **Equipment degrades** - Efficacy = base * (current/max health)
6. **Death just emits event** - JS handles respawn/loot/etc.
7. **Use timeouts, not intervals** - Variable timing based on skill checks
8. **Use existing Challenges type** - Leverages existing skill system infrastructure
9. **Status effects are lazy** - Expiry checked on access, not via timers
10. **Status effects emit events** - `statusApplied`, `statusExpired`, `statusTick`
11. **Optional timeout = permanent** - Implants are just status effects with no expiry
12. **Body configs define targetable parts** - Each body plan has different hit zones
13. **Stances have skill challenges** - Players can improve at using stances
14. **All configs persist in ServerConfig** - Same pattern as SkillConfigs
15. **Look shows body plan** - Visible body parts when examining creatures
