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

Defines body structure - what parts can be targeted, their modifiers.

```go
type BodyConfig struct {
    ID          string  // "humanoid", "quadruped", "serpent", etc.
    Description string

    Parts []BodyPart
    DefaultPart string  // Which part is targeted by default ("torso")
}

type BodyPart struct {
    ID               string   // "head", "torso", "leftArm", "tail", etc.
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
    ID          string  // "aggressive", "defensive", "evasive"
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
    ID          string
    Description string

    // Damage by type (e.g., {"physical": 10, "fire": 5} = 10 physical + 5 fire damage)
    DamageTypes  map[string]float64

    // Skill challenges (use existing Challenges type - results summed)
    // Each Challenge has {Skill, Level, Message}
    SpeedChallenges    Challenges  // e.g., [{Skill: "agility", Level: 10}, {Skill: "swordSpeed", Level: 5}]
    AccuracyChallenges Challenges  // For hit chance
    DamageChallenges   Challenges  // For damage bonus

    // Defense capabilities (per damage type - e.g., shield blocks physical well, not fire)
    CanParry          bool                    // Can redirect attacks (no damage)
    CanBlock          bool                    // Can absorb damage (weapon takes damage)
    ParryChallenges   map[string]Challenges   // damage type -> skill challenges for parry
    BlockChallenges   map[string]Challenges   // damage type -> skill challenges for block

    // Durability (0 = indestructible)
    MaxHealth       float64  // Maximum weapon health
    DegradationRate float64  // Multiplier for damage taken when blocking (e.g., 0.1 = 10% of blocked damage)
}
```

**Broken weapons:** When weapon health reaches 0, the weapon is useless - it cannot deal damage, parry, or block. It remains equipped but non-functional until repaired.

### ArmorConfig

```go
type ArmorConfig struct {
    ID          string
    Description string

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

**Broken armor:** When armor health reaches 0, the armor provides no protection. It remains equipped but non-functional until repaired.

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

1. **Weapon Check**:
   - If attacker's weapon health = 0, attack fails (broken weapon)

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

5. **Parry** (if weapon.CanParry and weapon health > 0):
   - For each damage type in attack, check `ParryChallenges[damageType]`
   - Defender rolls parry challenges
   - Add stance's `ParryModifier`
   - -> if `parryScore > hitScore`, attack is parried, no damage

6. **Block** (if weapon.CanBlock and weapon health > 0):
   - For each damage type in attack, check `BlockChallenges[damageType]`
   - Defender rolls block challenges
   - Add stance's `BlockModifier`
   - -> if `blockScore > hitScore`, attack is blocked:
     - Damage is negated
     - Blocking weapon takes degradation: `weaponDamage = blockedDamage * weapon.DegradationRate`

7. **Armor Soak** (if armor health > 0):
   - For each damage type, reduce by armor's reduction
   - Apply armor skill challenge result from `ArmorChallenges[damageType]`
   - Apply degradation: `actualReduction = baseReduction * challengeResult * (armorHealth / armorMaxHealth)`
   - Armor takes degradation: `armorDamage = soakedDamage * armor.DegradationRate`

8. **Critical Multiplier**:
   - If `isCrit` (from step 3), apply `CritDamageMultiplier` to remaining damage

9. **Body Part Damage Multiplier**:
   - Apply `bodyPart.DamageMultiplier` to final damage

10. **Apply Damage**:
    - Reduce defender's Health
    - If Health <= 0, emit `death` event

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
| `renderMiss` | Attacker's **weapon** | "Your sword swings wide" |
| `renderDodge` | **Defender** | Defender knows their dodge style |
| `renderParry` | Defender's **weapon** | Weapon did the parrying |
| `renderBlock` | Defender's **weapon** | Weapon did the blocking |
| `renderDamageDealt` | Attacker's **weapon** | "Your blade cuts deep" |
| `renderDamageReceived` | **Defender** | "Pain sears through you" |
| `renderArmorSoak` | Defender's **armor** | Armor knows its protection style |
| `renderCrit` | Attacker's **weapon** | Weapon knows its crit flavor |
| `renderDeath` | **Dying object** | They know their death style |
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
- `equipWeapon(objectID)` / `equipArmor(objectID)`
- `unequipWeapon()` / `unequipArmor()`
- `getWeaponHealth()` / `getArmorHealth()` - check equipment condition

**Stance:**
- `setStance(stanceConfigID)` / `getStance()`

**Status Effects:**
- `applyStatusEffect(configID, duration)` / `removeStatusEffect(id)`
- `getStatusEffects()` / `hasStatusEffect(configID)`

**JS events emitted:**
- Combat: `attackHit`, `attackMissed`, `parried`, `blocked`, `damaged`, `death`, `criticalHit`
- Status: `statusApplied`, `statusExpired`, `statusTick`
- Render (for message customization): `renderAttack`, `renderMiss`, `renderDodge`, `renderParry`, `renderBlock`, `renderDamageDealt`, `renderDamageReceived`, `renderArmorSoak`, `renderCrit`, `renderDeath`, `renderStatusApplied`, `renderStatusTick`, `renderStatusExpired`

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
7. **No weapon equipped**: Use unarmed defaults (fists - physical damage, no parry/block)
8. **No armor equipped**: No soak, full damage taken
9. **Damage type not in parry/block/armor map**: No defense against that type

---

## Key Design Decisions

1. **Attack skills have no recharge** - Weapon speed skill check controls timing
2. **Comparative defense** - Attacker's hit score compared against each defense score (dodge > hit = dodged)
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
