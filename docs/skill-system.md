----

# Planned Changes Summary

## 1. Game Time Context

Replace `ServerConfig` parameter in skill functions with `juicemud.Context`:

```go
type Context interface {
    context.Context
    Now() time.Time           // Game timeline - pauses when server stops
    ServerConfig() ServerConfig
}
```

Game time pauses when server is down, so skills don't decay/recharge during maintenance. The queue persists "current game time" periodically for recovery on restart.

## 2. Challenge Type Changes

- **OLD**: `Challenge` has single `Skill` string + `Level`
- **NEW**: `Challenge` has `map[string]bool` of skill names + single `Level`
- `Challenge` is now **only for static environmental checks** (e.g., perception to notice a hidden door)
- **Skill vs skill contests** (combat, opposed checks) do NOT use `Challenge` - they use direct skill rolls compared via `10 * log10(roll_A / roll_B)`

## 3. Two New Functions

### `EffectiveSkills(skills map[string]bool) float64`

Computes effective level for a set of skills. **Must be called immediately before Roll** since Roll updates LastUsedAt.

1. **Forget**: Apply forgetting formula (same sigmoid as current code), but **persist the lowered value to Practical**. This ensures decay is remembered until next use.

2. **Recharge**: Compute rechargeCoeff (same sigmoid + cumulative reuse as current code), then **fold into effective**: `effective = forgottenEffective + 10 * log10(rechargeCoeff)`

3. **Mean**: Compute arithmetic mean of all effective values: `mean = avg(eff_i)`. This is equivalent to geometric mean in linear power-space, ensuring skills 10+30 ≡ skills 20+20.

### `Roll(skills map[string]bool, precomputedEffective float64, opposingEffective float64) float64`

Rolls a result and handles side effects. Returns a value for comparison against other rolls.

1. **RNG**: Seed deterministically on user, target, time, and comma-sorted skill names

2. **Result**: Generate from `Uniform(0, 10^(precomputedEffective / 10))`

3. **Learn**: Same formula as today - improve Practical, bump Theoretical if exceeded. But:
   - theoryCoeff still uses `Theoretical - Practical` (faster learning when rusty)
   - challengeCoeff now uses `|effective - opposingEffective|` (optimal when evenly matched)

**Important**: `EffectiveSkills` and `Roll` must be called in tight sequence because `Roll` updates `LastUsedAt`.

## 4. Skill vs Skill Comparison

Compare rolls via: `score = 10 * log10(roll_A / roll_B)`

- **+10 = 10× better** (you dominated)
- **-10 = 10× worse** (they dominated)
- **0 = evenly matched**

Same scale as skill levels: +10 = 10× better everywhere.

### Combat Example

```go
// Compute all effective values first
attackEff := attacker.EffectiveSkills(map[string]bool{"attack": true})
parryEff  := defender.EffectiveSkills(map[string]bool{"parry": true})
dodgeEff  := defender.EffectiveSkills(map[string]bool{"dodge": true})
blockEff  := defender.EffectiveSkills(map[string]bool{"block": true})
soakEff   := defender.EffectiveSkills(map[string]bool{"soak": true})

// Roll all results
// Note: opposingEffective is for learning (challengeCoeff), not roll generation
// Attack skill learns vs best defense; each defense skill learns vs attack
bestDefense := max(parryEff, dodgeEff, blockEff, soakEff)
attackRoll := attacker.Roll(attackSkills, attackEff, bestDefense)
parryRoll  := defender.Roll(parrySkills, parryEff, attackEff)
dodgeRoll  := defender.Roll(dodgeSkills, dodgeEff, attackEff)
blockRoll  := defender.Roll(blockSkills, blockEff, attackEff)
soakRoll   := defender.Roll(soakSkills, soakEff, attackEff)

// Compare via log ratios (+10 = 10× better)
attackVsParry := 10 * math.Log10(attackRoll / parryRoll)
attackVsDodge := 10 * math.Log10(attackRoll / dodgeRoll)
attackVsBlock := 10 * math.Log10(attackRoll / blockRoll)
attackVsSoak  := 10 * math.Log10(attackRoll / soakRoll)

// Determine outcome: which defenses succeeded (score < 0)?
```

## 5. Configuration Changes

No new config fields needed. Existing `SkillConfig` unchanged.

----

# Skill and Challenge System

## Overview

The skill system models character competence in a way that creates meaningful gameplay while preventing exploitation. Skills improve through use, decay without practice, and require time to recover between uses.

## Core Concepts

### Skills

A skill represents a character's ability in a specific area. Each skill has:

- **Practical**: Current usable level (float32). This is what you can actually do right now.
- **Theoretical**: Maximum level achieved (float32). Your "muscle memory" ceiling - what you've proven capable of.
- **LastUsedAt**: Timestamp of last use. Used for recharge and forgetting calculations.
- **LastBase**: The recharge coefficient at last use. Enables cumulative fatigue tracking across rapid uses.

### Challenges

** CHANGES **
- OLD: Challenge has single Skill name + Level
- NEW: Challenge has `map[string]bool` of skill names + single Level
- NEW: Challenge is **only for static environmental checks** (e.g., notice hidden door, pick lock)
- NEW: **Skill vs skill contests** (combat, opposed checks) do NOT use Challenge - use direct rolls compared via `log10(roll_A / roll_B)`
- NEW: Multi-skill effective = arithmetic mean (geometric mean in power-space)
** END CHANGES **

A challenge is a skill check against a fixed difficulty level (not another character's skills):

- **Skills**: Map of skill names to check (map[string]bool). Effective = mean of all skills.
- **Level**: Difficulty (float32). Higher = harder.
- **Message**: Failure message shown to the user if the check fails.

## The Check Computation

### Step 1: Compute Effective Skill Level

** CHANGES **
- OLD: Effective only includes forget factor, Practical unchanged
- NEW: Forgetting **persists to Practical** (decay remembered until next use)
- NEW: Recharge folded into effective: `effective = forgottenEffective + 10 * log10(rechargeCoeff)`
- NEW: Recovery via learning (theoryCoeff), not instant restore
** END CHANGES **

The effective skill level accounts for both forgetting AND fatigue:

```go
effective = skill.Effective(timestamp)  // Includes both forget and recharge factors
```

**Forgetting (decay when unused):**

If the skill has a `Forget` duration configured:

```
nanosSinceLastUse = now - skill.LastUsedAt
forgetFraction = nanosSinceLastUse / skillConfig.Forget
forgetCoeff = 1 + (-1 / (1 + e^(8 - 8*forgetFraction))) + (1 / e^8)

permanentSkill = 0.5 * skill.Theoretical
forgettableSkill = skill.Practical - permanentSkill
forgottenEffective = forgettableSkill * forgetCoeff + permanentSkill
```

**Formula analysis - forgetCoeff:**
- **Range**: Approximately [0, 1]
- **Shape**: Sigmoid (S-curve) centered at `forgetFraction = 1` (i.e., when elapsed time equals the Forget duration)
- **At forgetFraction = 0** (just used): `forgetCoeff ≈ 1` (no decay)
- **At forgetFraction = 1** (one Forget period elapsed): `forgetCoeff ≈ 0.5`
- **At forgetFraction >> 1** (long unused): `forgetCoeff → 0`
- **Why sigmoid?** Forgetting isn't linear - you retain skills well initially, then decay accelerates, then plateaus as only "muscle memory" remains. The `e^8` terms shift and scale the curve to start near 1 and approach 0.

**Formula analysis - forgottenEffective:**
- **Range**: [`0.5 * skill.Theoretical`, `skill.Practical`]
- **Why this range?** You never forget everything - half your peak knowledge is permanently retained as "muscle memory." A master swordsman who hasn't practiced in decades is rusty but not a novice.

If no `Forget` is configured, `forgottenEffective = skill.Practical`.

** CHANGES **
- OLD: Decay computed from LastUsedAt, which resets after each use - decay doesn't truly persist
- NEW: Decay persists to Practical (in `EffectiveSkills()`) and stays until recovered via learning (theoryCoeff)
- NEW: Recharge folded into effective: `effective = forgottenEffective + 10 * log10(rechargeCoeff)`
** END CHANGES **

### Step 2: Compute Recharge Coefficient

** CHANGES **
- OLD: Recharge was applied as multiplier to success chance: `successChance = rechargeCoeff / (1 + 10^X)`
- NEW: Recharge is folded into effective skill: `effective += 10 * log10(rechargeCoeff)`
- This converts multiplicative probability penalty to additive skill penalty
- Result: fatigue reduces effective skill level, trivial tasks stay trivial when tired
- Formula details unchanged, just where it's applied changes
** END CHANGES **

Skills need recovery time between uses. The recharge coefficient penalizes rapid reuse:

```go
rechargeCoeff = skill.rechargeCoeff(timestamp)
```

If the skill has a `Recharge` duration configured:

```
nanosSinceLastUse = now - skill.LastUsedAt
rechargeFraction = nanosSinceLastUse / skillConfig.Recharge
baseRecharge = min(1, 0.5^(-(8*rechargeFraction - 8)) - 0.5^8)

cumulativeReuse = skill.LastBase * skillConfig.Reuse
rechargeCoeff = cumulativeReuse + (1 - cumulativeReuse) * baseRecharge
```

**Formula analysis - baseRecharge:**
- **Range**: [0, 1]
- **Shape**: Sigmoid (S-curve) that rises from 0 to 1
- **At rechargeFraction = 0** (just used): `baseRecharge ≈ 0` (completely exhausted)
- **At rechargeFraction = 0.5** (half recharged): `baseRecharge ≈ 0.004` (still very low)
- **At rechargeFraction = 0.875** (7/8 recharged): `baseRecharge ≈ 0.25`
- **At rechargeFraction = 1** (fully recharged): `baseRecharge ≈ 1`
- **Why this shape?** The steep rise near the end models "you need to wait for full recovery." Using a skill at 50% recharge is nearly as bad as using it immediately. This prevents "wait half the time, get half the benefit" exploits and encourages patience.

**Formula analysis - cumulativeReuse:**
- **Range**: [0, `skillConfig.Reuse`] where Reuse is typically 0-1
- **Purpose**: Prevents "timer reset" exploits from brief rests between uses.
- **Example**: With `Reuse = 0.5` and `Recharge = 1min`:
  - First swing: `rechargeCoeff = 1.0`, `LastBase = 1.0`
  - Wait 30s, swing again: `baseRecharge ≈ 0.004`, `cumulativeReuse = 0.5`, so `rechargeCoeff ≈ 0.5`
  - Wait 30s, swing again: `cumulativeReuse = 0.25`, so `rechargeCoeff ≈ 0.25`
  - Each brief rest doesn't reset the timer - fatigue accumulates
- **Why?** Without this, you could swing, wait 59 seconds, swing (nearly full power), wait 59 seconds, swing (nearly full power) indefinitely. With cumulative reuse, taking multiple too-short rests compounds the penalty. You must actually rest fully to recover fully.

**Formula analysis - rechargeCoeff:**
- **Range**: [0, 1]
- **Shape**: Interpolates between `cumulativeReuse` floor and 1
- **Why?** Combines immediate reuse penalty (`cumulativeReuse` sets the floor) with gradual recovery (`baseRecharge` climbs toward 1). Even with `Reuse = 0.5`, spamming eventually exhausts you as `LastBase` compounds downward.

If no `Recharge` is configured, `rechargeCoeff = 1.0`.

** CHANGES **
**Folding into effective:**
```
const MinValue = 1e-9  // Floor to prevent -Inf and division by zero

rechargeCoeff = max(rechargeCoeff, MinValue)
effectiveSkill = forgottenEffective + 10 * log10(rechargeCoeff)
```
- rechargeCoeff = 1.0: no penalty
- rechargeCoeff = 0.5: effective skill reduced by ~3
- rechargeCoeff = 0.1: effective skill reduced by 10
- rechargeCoeff = MinValue: effective skill reduced by 90
** END CHANGES **

### Step 3: Roll Generation (NEW)

** CHANGES **
- OLD: Compute logistic success chance, then roll against it
- NEW: Generate roll from `Uniform(0, 10^(effectiveMean / 10))`
- effectiveMean = arithmetic mean of all effective skills (geometric mean in power-space)
- Compare roll against challenge's roll or `10^(level / 10)` for static challenges
** END CHANGES **

For static challenges (fixed difficulty):
```
yourRoll = max(MinValue, Uniform(0, 10^(effectiveMean / 10)))
challengeRoll = max(MinValue, Uniform(0, 10^(challenge.Level / 10)))
score = 10 * log10(yourRoll / challengeRoll)
```

For skill vs skill (combat, opposed checks):
```
yourRoll = max(MinValue, Uniform(0, 10^(yourEffective / 10)))
theirRoll = max(MinValue, Uniform(0, 10^(theirEffective / 10)))
score = 10 * log10(yourRoll / theirRoll)
```

**Note**: `MinValue` (1e-9) floors prevent division by zero and `-Inf` results.

**Implementation**: The `Uniform(0, max)` distribution is generated using the deterministic RNG from Step 4: `roll = rng.Float64() * max`, then floored to `MinValue`.

**Score interpretation** (same scale as skill levels):
- **+10 = you rolled 10× better** (dominated)
- **-10 = they rolled 10× better** (you were dominated)
- **0 = evenly matched**

**Why this approach?**
- Enables one roll to be compared against multiple defenses (attack vs parry, dodge, block, soak)
- Equal effective levels give 50% win chance
- +10 effective levels ≈ 10× better max roll ≈ ~95% win chance

### Step 4: Generate Deterministic Random Value

** CHANGES **
- OLD: RNG seeded on single `skill.Name`
- NEW: RNG seeded on comma-separated sorted list of skill names from the challenge
- Example: Challenge with skills {stealth, agility} → seed includes "agility,stealth"
- This ensures the same multi-skill challenge produces consistent results within duration window
** END CHANGES **

```go
random = skillUse.rng().Float64()  // Returns value in [0, 1)
```

The RNG is seeded deterministically based on:
- `skillUse.user`: Who is attempting
- Sorted skill names: Comma-separated list of all skills in the challenge (e.g., "agility,stealth")
- `skillUse.target`: Against what/whom
- Time step: Based on `skillConfig.Duration` (see Duration section below)

**Why deterministic?** Some skill checks logically remain valid for a period of time. If you're hiding in a room and someone looks around, the hide check shouldn't be re-rolled every time they type "look" - that would be both illogical and exploitable. The deterministic window means the same hiding attempt against the same observer produces the same result until enough time passes that circumstances have meaningfully changed.

### Step 5: Compute Score

** CHANGES **
- OLD: `result = -10 * log10(random / successChance)`
- NEW: `score = 10 * log10(yourRoll / theirRoll)`
- Same +10 = 10× scale as skill levels
** END CHANGES **

```
score = 10 * log10(yourRoll / theirRoll)
```

**Score examples** (you effective 20 vs them effective 20, both max at 100):
| yourRoll | theirRoll | score | meaning |
|----------|-----------|-------|---------|
| 100 | 10 | +10 | you dominated |
| 50 | 50 | 0 | evenly matched |
| 10 | 100 | -10 | they dominated |
| 100 | 1 | +20 | crushing victory |

**Score examples** (you effective 30 vs them effective 20):
- Your max: 10^3 = 1000, Their max: 10^2 = 100
- You roll higher on average, winning ~95% of the time
- Typical winning score: +5 to +15

### Step 6: Update Skill State

After the check:

```go
skill.LastBase = rechargeCoeff      // Remember fatigue level
skill.LastUsedAt = now              // Mark usage time
```

If learning is enabled (`improve = true`), the skill also gains experience (see Skill Improvement section).

## Multi-Skill Challenges

** CHANGES **
- OLD: `Challenges` was a slice of `Challenge`, each with one skill + one level
- OLD: Each challenge rolled separately, results summed
- NEW: Single `Challenge` contains `map[string]bool` of skills + one level
- NEW: Compute arithmetic mean of effective skills (= geometric mean in power-space)
- NEW: Challenge only for static environmental checks; skill vs skill uses direct rolls
** END CHANGES **

A challenge can require multiple skills. The effective level is the **arithmetic mean** of all skills (equivalent to geometric mean in power-space, ensuring 10+30 ≡ 20+20):

```go
// For static challenges only - skill vs skill uses EffectiveSkills + Roll directly
func (c *Challenge) Check(challenger *Object, targetID string) float64 {
    effectiveMean := challenger.EffectiveSkills(c.Skills)

    yourRoll := max(MinValue, Uniform(0, 10^(effectiveMean / 10)))
    challengeRoll := max(MinValue, Uniform(0, 10^(c.Level / 10)))

    return 10 * log10(yourRoll / challengeRoll)
}
```

Overall success if `score > 0`.

**Example: Noticing a hidden door (static challenge)**
- Challenge skills: {perception, investigation}
- Challenge level: 15 (fixed difficulty)
- Your effective mean: (perception 18 + investigation 12) / 2 = 15
- Evenly matched → ~50% success

**For skill vs skill (e.g., hiding from observer):**
Use `EffectiveSkills` + `Roll` directly, not `Challenge`. See combat example in notes.

## Duration (Deterministic Windows)

** CHANGES **
- OLD: Duration keyed on single skill name
- NEW: Duration keyed on the full combination of sorted skill names
- The sorted skill names string (e.g., "agility,stealth") is used to look up the duration
- This means the same multi-skill challenge has its own duration window
** END CHANGES **

Some checks should be consistent within a time window. If you're examining a lock, checking it twice in the same second shouldn't give different answers.

When `skillConfig.Duration` is set, the RNG seed includes a time step:

```
step = now.UnixNano() / skillConfig.Duration / 3
```

Plus a random offset to prevent predictable boundaries:

```
offset = initialRng.Int63n(skillConfig.Duration)
finalStep = (now.UnixNano() + offset) / skillConfig.Duration / 3
```

**Why divide by 3?** Creates overlapping windows. With just `now / duration`, there would be hard boundaries where results change. The offset and division by 3 create fuzzy boundaries - you can't predict exactly when the window shifts, preventing timing exploits.

**What this provides:**
- Logical consistency: A hidden character stays hidden (or detected) for a reasonable duration
- No "look" spam exploits: Typing "look" repeatedly doesn't re-roll detection checks
- Immersion: The world behaves consistently rather than flickering between states

**Default**: If no `Duration` is configured, it defaults to 0, meaning each check generates a fresh random seed (no deterministic window).

## Skill Improvement

** CHANGES **
- OLD: challengeCoeff used `|challenge.Level - effective|` (single skill vs fixed level)
- NEW: challengeCoeff uses `|effective - opposingEffective|` (your current vs their current)
- theoryCoeff unchanged: faster learning when rusty (`Theoretical - Practical`)
- Same pattern: improve Practical, bump Theoretical if exceeded
** END CHANGES **

When `improve = true`, skills grow through use:

```go
improvement = rechargeCoeff * skillCoeff * theoryCoeff * challengeCoeff * perUse
skill.Practical += improvement
if skill.Practical > skill.Theoretical {
    skill.Theoretical = skill.Practical
}
```

**Formula analysis - skillCoeff:**

```
skillCoeff = 0.0355 * 0.9^effective
```

- **Range**: Decreasing exponential, starts around 0.0355 at level 0
- **At effective = 0**: `skillCoeff ≈ 0.0355`
- **At effective = 10**: `skillCoeff ≈ 0.0124`
- **At effective = 20**: `skillCoeff ≈ 0.0043`
- **Why exponential decay?** Diminishing returns - the better you are, the harder it is to improve. Going from 0→1 is much easier than 19→20. This prevents runaway skill growth and makes high-level characters feel earned.

**Formula analysis - theoryCoeff:**

```
theoryCoeff = max(1, 1 + 3*(skill.Theoretical - skill.Practical))
```

- **Range**: [1, ∞) but practically [1, ~10]
- **At Theoretical = Practical**: `theoryCoeff = 1` (normal learning)
- **At Theoretical = Practical + 3**: `theoryCoeff = 10` (10x faster relearning)
- **Why?** Relearning is faster than initial learning. If your theoretical is 15 but you've decayed to practical 10, you should recover faster than someone learning from scratch. "It's like riding a bike."

**Formula analysis - challengeCoeff:**

** CHANGES **
- OLD: `challengeCoeff = 1 / (1 + |challenge.Level - effective|)` (your effective vs fixed level)
- NEW: `challengeCoeff = 1 / (1 + |effective - opposingEffective|)` (your current vs their current)
** END CHANGES **

```
challengeCoeff = 1 / (1 + |effective - opposingEffective|)
```

- **Range**: (0, 1]
- **At effective = opposingEffective**: `challengeCoeff = 1` (optimal learning)
- **At |difference| = 1**: `challengeCoeff = 0.5`
- **At |difference| = 9**: `challengeCoeff = 0.1`
- **Why?** You learn best from appropriate challenges. Fighting someone at your current level teaches the most. Too weak or too strong opponents don't challenge you meaningfully.

**Formula analysis - perUse:**

```
perUse = skillConfig.Recharge / 6min
```

- **Range**: Proportional to recharge time
- **At Recharge = 6min**: `perUse = 1`
- **At Recharge = 12min**: `perUse = 2`
- **Why?** Skills with longer recharge times give more XP per use. A powerful spell you can cast once per hour should advance your skill more than a cantrip you spam every second. This keeps different skill types balanced.

**Combined effect:**
- Can't grind easy challenges (low `challengeCoeff`)
- Can't spam attempts (low `rechargeCoeff`)
- Diminishing returns at high levels (low `skillCoeff`)
- Relearning is faster (high `theoryCoeff`)
- Powerful skills advance faster per use (high `perUse`)

If practical exceeds theoretical, theoretical is raised to match - you've proven new capability.

## Application: Descriptions and Detection

Objects have multiple descriptions with associated challenges. When viewing an object:

1. Each description has `Challenge` requirements (static environmental checks)
2. Viewer's effective skills are computed and rolled against the challenge
3. If score > 0, that description is visible
4. First matching description is shown

This enables:
- Hidden objects (require Perception checks to notice)
- Disguises (require Insight to see through)
- Graduated detail (basic description vs. expert analysis)
- Multi-skill requirements (noticing something requires both Perception AND Knowledge)

## Configuration

Per-skill configuration via `SkillConfigs`:

```go
type SkillConfig struct {
    Duration SkillDuration  // Time window for deterministic checks
    Recharge SkillDuration  // Time to fully recover between uses
    Reuse    float64        // Effectiveness retained on immediate reuse (0-1)
    Forget   SkillDuration  // Time to decay to 50% of theoretical
}
```

## Edge Cases

**Empty skills map**: If `Challenge.Skills` is empty, the effective level is 0 (mean of empty set). This produces rolls in `Uniform(0, 1)`.

**Never-used skill** (`LastUsedAt = 0`): Only occurs for brand-new skills where `Practical = 0` and `Theoretical = 0`. Since there's nothing to forget, `forgottenEffective = 0`. The skill starts fresh.

**`Theoretical < Practical`**: Can only happen immediately after `Practical` is bumped by learning. The next line `if skill.Practical > skill.Theoretical { skill.Theoretical = skill.Practical }` corrects this. During this brief moment, `theoryCoeff = max(1, 1 + 3*(negative)) = 1`, so no special handling needed.

**Skill level 0**: Produces rolls in `Uniform(0, 10^0) = Uniform(0, 1)`. Still functional, just very weak.

**Negative effective** (due to extreme recharge penalty): Possible if `rechargeCoeff` is very low. For example, `forgottenEffective = 10` and `rechargeCoeff = 0.001` gives `effective = 10 + 10*log10(0.001) = 10 - 30 = -20`. This produces rolls in `Uniform(0, 10^(-2)) = Uniform(0, 0.01)`. Very weak but still functional due to `MinValue` floor.

## Design Rationale

1. **Logical consistency**: Deterministic RNG within time windows means skill checks (like hiding or perception) remain valid for a duration rather than flickering with each command.

2. **Meaningful choices**: Recharge times make skill use a resource to manage, not spam.

3. **Natural learning curve**: Diminishing returns and appropriate-challenge bonuses encourage organic progression.

4. **Balanced multi-skill handling**: Using mean (not sum) ensures a 3-skill check doesn't require abnormally high opposing skill. Since learning is harder at high values, sum would create unfair matchups.

5. **Flexible comparisons**: Uniform distribution rolls can be compared against multiple opponents (one attack roll vs parry, dodge, block, soak).

6. **Quality over binary**: Knowing *how well* you succeeded enables richer game mechanics than simple pass/fail.
