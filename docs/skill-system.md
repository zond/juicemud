# Skill and Challenge System

## Overview

The skill system models character competence in a way that creates meaningful gameplay while preventing exploitation. Skills improve through use, decay without practice, and require time to recover between uses.

## Core Concepts

### Game Time Context

All skill operations use `structs.Context` which provides game time that pauses when the server is down:

```go
type Context interface {
    context.Context
    Now() time.Time           // Game time (may differ from wall clock)
    ServerConfig() *ServerConfig
}
```

Game time pauses when server is down, so skills don't decay/recharge during maintenance. The queue persists "current game time" periodically (to `<storage>/gametime`) for recovery on restart.

### Skills

A skill represents a character's ability in a specific area. Each skill has:

- **Practical**: Current usable level (float32). This is what you can actually do right now.
- **Theoretical**: Maximum level achieved (float32). Your "muscle memory" ceiling - what you've proven capable of.
- **LastUsedAt**: Timestamp of last use. Used for recharge and forgetting calculations.
- **LastBase**: The recharge coefficient at last use. Enables cumulative fatigue tracking across rapid uses.

### Challenges

A challenge is a skill check against a fixed difficulty level (not another character's skills):

- **Skills**: Map of skill names to check (`map[string]bool`). Effective = mean of all skills.
- **Level**: Difficulty (float32). Higher = harder.
- **Message**: Failure message shown to the user if the check fails.

**Important**: Challenges are only for static environmental checks (e.g., notice hidden door, pick lock). For skill vs skill contests (combat, opposed checks), use `EffectiveSkills` + `Roll` directly and compare rolls.

## Two Fundamental Methods

### `EffectiveSkills(ctx Context, skills map[string]bool) float64`

Computes the mean effective level for a set of skills. **Must be called immediately before Roll** since this method persists forgetting decay to Practical.

For each skill:
1. **Forget**: Apply forgetting formula and **persist the lowered value to Practical**
2. **Recharge**: Fold recharge penalty into effective: `effective = Practical + 10 * log10(rechargeCoeff)`

Returns the arithmetic mean of all effective values (equivalent to geometric mean in power-space, ensuring skills 10+30 ≡ skills 20+20).

### `Roll(ctx Context, skills map[string]bool, target string, precomputedEffective, opposingEffective float64, rng *rnd.Rand) float64`

Generates a uniform roll and handles side effects. Returns a value for comparison against other rolls.

1. **RNG**: Use provided RNG or seed deterministically on user, target, time, and comma-sorted skill names
2. **Result**: Generate from `Uniform(0, 10^(precomputedEffective / 10))`, floored to `MinValue` (1e-9)
3. **Learn**: If `object.Learning` is enabled, apply recovery and growth (see Learning section)
4. **Update state**: Set `LastBase = rechargeCoeff`, `LastUsedAt = now`

**Important**: `EffectiveSkills` and `Roll` must be called in tight sequence because:
- `EffectiveSkills` persists forgetting to Practical
- `Roll` updates `LastUsedAt` which affects future forgetting/recharge calculations

## The Check Computation

### Step 1: Compute Effective Skill Level

The effective skill level accounts for both forgetting AND fatigue:

**Forgetting (decay when unused):**

If the skill has a `Forget` duration configured:

```
nanosSinceLastUse = now - skill.LastUsedAt
forgetFraction = nanosSinceLastUse / skillConfig.Forget
forgetCoeff = 1 + (-1 / (1 + e^(8 - 8*forgetFraction))) + (1 / e^8)

permanentSkill = 0.5 * skill.Theoretical
forgettableSkill = skill.Practical - permanentSkill
forgottenPractical = forgettableSkill * forgetCoeff + permanentSkill
```

**Formula analysis - forgetCoeff:**
- **Range**: Approximately [0, 1]
- **Shape**: Sigmoid (S-curve) centered at `forgetFraction = 1` (when elapsed time equals Forget duration)
- **At forgetFraction = 0** (just used): `forgetCoeff ≈ 1` (no decay)
- **At forgetFraction = 1** (one Forget period elapsed): `forgetCoeff ≈ 0.5`
- **At forgetFraction >> 1** (long unused): `forgetCoeff → 0`
- **Why sigmoid?** Forgetting isn't linear - you retain skills well initially, then decay accelerates, then plateaus as only "muscle memory" remains.

**Formula analysis - forgottenPractical:**
- **Range**: [`0.5 * skill.Theoretical`, `skill.Practical`]
- **Why this range?** You never forget everything - half your peak knowledge is permanently retained as "muscle memory."

**Forgetting persists to Practical**: When `EffectiveSkills` is called, the decayed value is written back to `skill.Practical`. This ensures decay is remembered until recovered via learning.

If no `Forget` is configured, `forgottenPractical = skill.Practical`.

### Step 2: Compute Recharge Coefficient

Skills need recovery time between uses. The recharge coefficient penalizes rapid reuse:

If the skill has a `Recharge` duration configured:

```
nanosSinceLastUse = now - skill.LastUsedAt
rechargeFraction = nanosSinceLastUse / skillConfig.Recharge
baseRecharge = min(1, rechargeFraction^2)

cumulativeReuse = skill.LastBase * skillConfig.Reuse
rechargeCoeff = cumulativeReuse + (1 - cumulativeReuse) * baseRecharge
```

**Formula analysis - baseRecharge (square curve):**
- **Range**: [0, 1]
- **Shape**: Quadratic curve that rises slowly then accelerates
- **At rechargeFraction = 0** (just used): `baseRecharge = 0` (completely exhausted)
- **At rechargeFraction = 0.5** (half recharged): `baseRecharge = 0.25`
- **At rechargeFraction = 0.75** (3/4 recharged): `baseRecharge ≈ 0.56`
- **At rechargeFraction = 1** (fully recharged): `baseRecharge = 1`
- **Why square curve?** Encourages waiting for fuller recovery. Using a skill at 50% recharge gives only 25% effectiveness.

**Formula analysis - cumulativeReuse:**
- **Range**: [0, `skillConfig.Reuse`] where Reuse is typically 0-1
- **Purpose**: Prevents "timer reset" exploits from brief rests between uses.
- **Example**: With `Reuse = 0.5` and `Recharge = 1min`:
  - First swing: `rechargeCoeff = 1.0`, `LastBase = 1.0`
  - Wait 30s, swing again: `baseRecharge = 0.25`, `cumulativeReuse = 0.5`, so `rechargeCoeff ≈ 0.625`
  - Wait 30s, swing again: `cumulativeReuse = 0.3125`, so `rechargeCoeff ≈ 0.48`
  - Each brief rest doesn't reset the timer - fatigue accumulates

If no `Recharge` is configured, `rechargeCoeff = 1.0`.

**Folding into effective:**
```
const MinValue = 1e-9  // Floor to prevent -Inf and division by zero

rechargeCoeff = max(rechargeCoeff, MinValue)
effectiveSkill = forgottenPractical + 10 * log10(rechargeCoeff)
```
- rechargeCoeff = 1.0: no penalty
- rechargeCoeff = 0.5: effective skill reduced by ~3
- rechargeCoeff = 0.1: effective skill reduced by 10
- rechargeCoeff = MinValue: effective skill reduced by 90

### Step 3: Roll Generation

For static challenges (fixed difficulty), the `Challenge.Check` method:
```go
func (c *Challenge) Check(ctx Context, challenger *Object, targetID string, rng *rnd.Rand) float64
```

```
effectiveMean = challenger.EffectiveSkills(ctx, c.Skills)
yourRoll = max(MinValue, rng.Float64() * 10^(effectiveMean / 10))
challengeRoll = max(MinValue, rng.Float64() * 10^(c.Level / 10))
score = 10 * log10(yourRoll / challengeRoll)
```

If the challenge has no skills (`!c.HasChallenge()`), returns 1 (automatic success).

### Step 4: Generate Deterministic Random Value

The RNG is seeded deterministically based on:
- `user`: Who is attempting (object ID)
- Sorted skill names: Comma-separated list via `SkillsKey()` (e.g., "agility,stealth")
- `target`: Against what/whom (object ID)
- Time step: Based on challenge duration (see Duration section)

**Why deterministic?** Some skill checks logically remain valid for a period of time. If you're hiding in a room and someone looks around, the hide check shouldn't be re-rolled every time they type "look".

### Step 5: Compute Score

```
score = 10 * log10(yourRoll / theirRoll)
```

**Score interpretation** (same scale as skill levels):
- **+10 = you rolled 10× better** (dominated)
- **-10 = they rolled 10× better** (you were dominated)
- **0 = evenly matched**

Success if `score > 0`.

### Step 6: Update Skill State

After the check (handled by `Roll`):

```go
skill.LastBase = rechargeCoeff      // Remember fatigue level
skill.LastUsedAt = now              // Mark usage time
```

If learning is enabled, recovery and growth are also applied (see Learning section).

## Skill vs Skill Comparison

For combat and opposed checks, **do NOT use Challenge**. Instead use `EffectiveSkills` + `Roll` directly:

```
yourRoll = max(MinValue, Uniform(0, 10^(yourEffective / 10)))
theirRoll = max(MinValue, Uniform(0, 10^(theirEffective / 10)))
score = 10 * log10(yourRoll / theirRoll)
```

**Score meaning:**
- **+10 = you rolled 10× better** (dominated)
- **-10 = they rolled 10× better** (you were dominated)
- **0 = evenly matched**

### Combat Example: Attack vs Multiple Defenses

The key advantage of the roll-based system is that **one attack roll can be compared against multiple defense rolls**:

```go
// Compute all effective values first (this persists forgetting)
attackEff := attacker.EffectiveSkills(ctx, map[string]bool{"swords": true})
parryEff  := defender.EffectiveSkills(ctx, map[string]bool{"parry": true})
dodgeEff  := defender.EffectiveSkills(ctx, map[string]bool{"dodge": true})
blockEff  := defender.EffectiveSkills(ctx, map[string]bool{"block": true})

// Generate rolls
// Note: opposingEffective affects learning, not roll generation
attackRoll := attacker.Roll(ctx, map[string]bool{"swords": true}, defender.Id,
                            attackEff, max(parryEff, dodgeEff, blockEff), nil)
parryRoll  := defender.Roll(ctx, map[string]bool{"parry": true}, attacker.Id,
                            parryEff, attackEff, nil)
dodgeRoll  := defender.Roll(ctx, map[string]bool{"dodge": true}, attacker.Id,
                            dodgeEff, attackEff, nil)
blockRoll  := defender.Roll(ctx, map[string]bool{"block": true}, attacker.Id,
                            blockEff, attackEff, nil)

// Compare via log ratios
attackVsParry := 10 * math.Log10(attackRoll / parryRoll)  // >0 = attack beats parry
attackVsDodge := 10 * math.Log10(attackRoll / dodgeRoll)  // >0 = attack beats dodge
attackVsBlock := 10 * math.Log10(attackRoll / blockRoll)  // >0 = attack beats block

// Determine outcome: attack succeeds only if it beats ALL active defenses
// Defender chooses which defenses to attempt (each costs stamina/time)
if attackVsParry > 0 && attackVsDodge > 0 && attackVsBlock > 0 {
    // Attack hits - apply damage
} else {
    // Defended - highest successful defense determines how
}
```

**Why this approach?**
- **One roll, multiple comparisons**: Generate one attack roll, compare against each defense
- **Defender choice matters**: Defender chooses which defensive skills to use
- **Fair comparison**: Equal effective levels give 50% win chance
- **Scale consistency**: +10 effective levels ≈ 10× better roll ≈ ~95% win chance

## Multi-Skill Challenges

A challenge can require multiple skills. The effective level is the **arithmetic mean** of all skills:

```go
effectiveMean = sum(effective_i) / count
```

This is equivalent to geometric mean in power-space, ensuring skills 10+30 ≡ skills 20+20.

**Example: Noticing a hidden door (static challenge)**
- Challenge skills: {perception, investigation}
- Challenge level: 15 (fixed difficulty)
- Your effective mean: (perception 18 + investigation 12) / 2 = 15
- Evenly matched → ~50% success

## Challenge Methods

### `HasChallenge() bool`

Returns true if the challenge has any skills defined. Used to detect "no challenge" cases.

### `Check(ctx Context, challenger *Object, targetID string, rng *rnd.Rand) float64`

Tests the challenger's skills against the challenge difficulty. Returns score (positive = success).

If `rng` is nil, generates one internally. If provided, uses it (allows `CheckWithDetails` to continue the sequence for blame).

### `CheckWithDetails(ctx Context, challenger *Object, targetID string) (float64, string)`

Like `Check` but also returns the name of a "blamed" skill on failure. Blame is probabilistic, weighted by inverse effective level (weaker skills are blamed more often).

Used by `renderExitFailed` callbacks to provide informative failure messages like "Your weak sorcery prevents you from passing!"

### `Merge(other Challenge) Challenge`

Combines two challenges by:
- Unioning their Skills maps
- Adding their Levels (stacked challenges are harder)
- Keeping first non-empty Message

Used when exit challenges or description challenges stack.

## Duration (Deterministic Windows)

Some checks should be consistent within a time window. If you're examining a lock, checking it twice in the same second shouldn't give different answers.

Challenge durations are stored in `ServerConfig.challengeDurations` keyed by `SkillsKey()` (comma-sorted skill names). When a duration is configured:

```
step = (now.UnixNano() + offset) / duration.Nanoseconds() / 3
```

The offset is randomized per-check to prevent predictable boundaries. The division by 3 creates overlapping windows.

**What this provides:**
- Logical consistency: A hidden character stays hidden (or detected) for a reasonable duration
- No "look" spam exploits: Typing "look" repeatedly doesn't re-roll detection checks
- Immersion: The world behaves consistently rather than flickering between states

**Default**: If no duration is configured, each check generates a fresh random seed.

## Skill Improvement (Learning)

Learning is split into two parts with different requirements:

### Recovery (Practical catching up to Theoretical)

```go
gap = Theoretical - Practical
if gap > 0 {
    recoveryGain = 0.05 * rechargeCoeff * gap
}
skill.Practical += recoveryGain
```

- **Always applies** when Practical < Theoretical (even if `object.Learning` is false)
- **5% of gap** per fully-recharged use
- **Doesn't depend on challenge difficulty** - you recover muscle memory regardless
- **Scales with recharge** - spamming doesn't help recovery
- **Compensates for forgetting** - NPCs with Learning disabled won't permanently decay

### Growth (Theoretical increases)

Only applies when `object.Learning` is enabled:

```go
upToSpeedCoeff = 1 / (1 + gap)           // Must be at your peak to grow
challengeCoeff = 1 / (1 + challengeGap)   // Must face appropriate difficulty
skillCoeff = 0.0355 * 0.9^Theoretical     // Diminishing returns at high levels

growthGain = rechargeCoeff * skillCoeff * upToSpeedCoeff * challengeCoeff
skill.Practical += growthGain
skill.Theoretical += growthGain
```

**Formula analysis - upToSpeedCoeff:**
- **Range**: (0, 1]
- **At gap = 0** (Practical = Theoretical): `upToSpeedCoeff = 1` (full learning)
- **At gap = 1**: `upToSpeedCoeff = 0.5`
- **At gap = 9**: `upToSpeedCoeff = 0.1`
- **Why?** You must be "warmed up" to your full capability to truly grow. Rusty skills need recovery first.

**Formula analysis - challengeCoeff:**
- **Range**: (0, 1]
- **At Theoretical = opposingEffective**: `challengeCoeff = 1` (optimal learning)
- **At |difference| = 1**: `challengeCoeff = 0.5`
- **At |difference| = 9**: `challengeCoeff = 0.1`
- **Why?** You learn best from appropriate challenges. Too easy or too hard doesn't teach.

**Formula analysis - skillCoeff:**
- **Range**: Decreasing exponential
- **At Theoretical = 0**: `skillCoeff ≈ 0.0355`
- **At Theoretical = 10**: `skillCoeff ≈ 0.0124`
- **At Theoretical = 20**: `skillCoeff ≈ 0.0043`
- **Why?** Diminishing returns - the better you are, the harder it is to improve.

**Combined effect:**
- Can't grind easy challenges (low `challengeCoeff`)
- Can't spam attempts (low `rechargeCoeff`)
- Diminishing returns at high levels (low `skillCoeff`)
- Must recover before growing (low `upToSpeedCoeff` when rusty)

## Configuration

### SkillConfig (per-skill)

```go
type SkillConfig struct {
    Recharge SkillDuration  // Time to fully recover between uses (0 = no cooldown)
    Reuse    float64        // Effectiveness retained on immediate reuse (0-1)
    Forget   SkillDuration  // Time to decay to 50% of theoretical (0 = no decay)
}
```

Accessed via `ServerConfig.GetSkillConfig(skillName)`. Unconfigured skills get defaults:
- Recharge: 0 (no cooldown)
- Forget: 2 months (skills decay without practice)
- Reuse: 0 (no carryover of depleted state)

### ChallengeDuration (per-skill-combination)

Stored in `ServerConfig.challengeDurations` keyed by `SkillsKey()`:

```go
// Get duration for a skill combination (returns 0 if not configured)
duration := cfg.GetChallengeDuration(skillsKey)

// Set duration for a skill combination
cfg.SetChallengeDuration(SkillsKey(map[string]bool{"perception": true, "stealth": true}), Duration(time.Minute))
```

When a duration is configured, checks using that skill combination produce deterministic results within overlapping time windows (see Duration section). When 0, each check generates a fresh random seed.

## Application: Descriptions and Detection

Objects have multiple descriptions with associated challenges. When viewing an object:

1. Each description has `Challenge` requirements (static environmental checks)
2. Viewer's effective skills are computed and rolled against the challenge
3. If score > 0, that description is visible
4. All matching descriptions are included (not just first)

This enables:
- Hidden objects (require Perception checks to notice)
- Disguises (require Insight to see through)
- Graduated detail (basic description vs. expert analysis)
- Multi-skill requirements (noticing something requires both Perception AND Knowledge)

## Edge Cases

**Empty skills map**: `EffectiveSkills` returns 0 (mean of empty set). `Challenge.Check` with no skills (`!HasChallenge()`) returns 1 (automatic success).

**Never-used skill** (`LastUsedAt = 0`): No forgetting or recharge penalties apply. Skill starts fresh.

**`Theoretical < Practical`**: Can only happen briefly during learning. The next use will have `gap < 0`, so `recoveryGain = 0` and `upToSpeedCoeff > 1` (capped implicitly by other factors). Not a problem in practice.

**Skill level 0**: Produces rolls in `Uniform(0, 10^0) = Uniform(0, 1)`. Still functional, just very weak.

**Negative effective** (due to extreme recharge penalty): Possible if `rechargeCoeff` is very low. For example, `Practical = 10` and `rechargeCoeff = 0.001` gives `effective = 10 + 10*log10(0.001) = 10 - 30 = -20`. This produces rolls in `Uniform(0, 10^(-2)) = Uniform(0, 0.01)`. Very weak but still functional due to `MinValue` floor.

## Design Rationale

1. **Logical consistency**: Deterministic RNG within time windows means skill checks (like hiding or perception) remain valid for a duration rather than flickering with each command.

2. **Meaningful choices**: Recharge times make skill use a resource to manage, not spam.

3. **Natural learning curve**: Diminishing returns and appropriate-challenge bonuses encourage organic progression.

4. **Forgetting persists**: Decay is remembered until recovered via learning, not instantly restored on next use.

5. **Recovery before growth**: Must be "warmed up" to peak capability before you can improve further.

6. **Balanced multi-skill handling**: Using mean (not sum) ensures a 3-skill check doesn't require abnormally high total skill.

7. **Flexible comparisons**: One roll can be compared against multiple opponents (one attack roll vs parry, dodge, block).

8. **Quality over binary**: Knowing *how well* you succeeded enables richer game mechanics than simple pass/fail.
