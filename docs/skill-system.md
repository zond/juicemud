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

A challenge is a skill check at a specific difficulty level:

- **Skill**: Which skill to test against (string name).
- **Level**: Difficulty (float32). Higher = harder. A level 10 challenge is appropriate for a level 10 skill.
- **Message**: Failure message shown to the user if the check fails. Potentially a template string populated by the skill system (exact semantics TBD).

## The Check Computation

### Step 1: Compute Effective Skill Level

The effective skill level accounts for forgetting - skills decay when unused:

```go
effective = skill.Effective(timestamp)
```

If the skill has a `Forget` duration configured:

```
nanosSinceLastUse = now - skill.LastUsedAt
forgetFraction = nanosSinceLastUse / skillConfig.Forget
forgetCoeff = 1 + (-1 / (1 + e^(8 - 8*forgetFraction))) + (1 / e^8)

permanentSkill = 0.5 * skill.Theoretical
forgettableSkill = skill.Practical - permanentSkill
effective = forgettableSkill * forgetCoeff + permanentSkill
```

**Formula analysis - forgetCoeff:**
- **Range**: Approximately [0, 1]
- **Shape**: Sigmoid (S-curve) centered at `forgetFraction = 1` (i.e., when elapsed time equals the Forget duration)
- **At forgetFraction = 0** (just used): `forgetCoeff ≈ 1` (no decay)
- **At forgetFraction = 1** (one Forget period elapsed): `forgetCoeff ≈ 0.5`
- **At forgetFraction >> 1** (long unused): `forgetCoeff → 0`
- **Why sigmoid?** Forgetting isn't linear - you retain skills well initially, then decay accelerates, then plateaus as only "muscle memory" remains. The `e^8` terms shift and scale the curve to start near 1 and approach 0.

**Formula analysis - effective:**
- **Range**: [`0.5 * skill.Theoretical`, `skill.Practical`]
- **Why this range?** You never forget everything - half your peak knowledge is permanently retained as "muscle memory." A master swordsman who hasn't practiced in decades is rusty but not a novice.

If no `Forget` is configured, `effective = skill.Practical`.

### Step 2: Compute Recharge Coefficient

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

### Step 3: Compute Success Chance

```
successChance = rechargeCoeff / (1 + 10^((challenge.Level - effective) * 0.1))
```

**Formula analysis:**
- **Range**: [0, `rechargeCoeff`], practically (0, 1) when fully recharged
- **Shape**: Logistic curve in `(challenge.Level - effective)`
- **At challenge.Level = effective**: `successChance = rechargeCoeff / 2` (50% base, modified by fatigue)
- **At challenge.Level = effective + 10**: `successChance ≈ rechargeCoeff / 11 ≈ 9%`
- **At challenge.Level = effective - 10**: `successChance ≈ rechargeCoeff * 10/11 ≈ 91%`
- **At challenge.Level = effective + 20**: `successChance ≈ 1%`
- **At challenge.Level = effective - 20**: `successChance ≈ 99%`
- **Why 0.1 multiplier?** Makes 10 levels equal one order of magnitude in odds. A level 20 character vs level 10 challenge has 10:1 odds. This feels intuitive and scales well.
- **Why logistic?** Smooth curve, bounded output, symmetric around the inflection point. No matter how skilled you are, there's always some failure chance; no matter how outmatched, there's always some hope.

### Step 4: Generate Deterministic Random Value

```go
random = skillUse.rng().Float64()  // Returns value in [0, 1)
```

The RNG is seeded deterministically based on:
- `skillUse.user`: Who is attempting
- `skill.Name`: Which skill
- `skillUse.target`: Against what/whom
- Time step: Based on `skillConfig.Duration` (see Duration section below)

**Why deterministic?** Some skill checks logically remain valid for a period of time. If you're hiding in a room and someone looks around, the hide check shouldn't be re-rolled every time they type "look" - that would be both illogical and exploitable. The deterministic window means the same hiding attempt against the same observer produces the same result until enough time passes that circumstances have meaningfully changed.

### Step 5: Compute Final Result

```
result = -10 * log10(random / successChance)
```

**Formula analysis:**
- **Range**: Theoretically (-∞, +∞), practically around [-30, +30] for typical values
- **Unified formula**: The same formula handles both success and failure cases:
  - When `random < successChance`: ratio < 1, log is negative, result is **positive** (success)
  - When `random > successChance`: ratio > 1, log is positive, result is **negative** (failure)
  - When `random = successChance`: ratio = 1, result = **0** (boundary)
- **Success probability**: Exactly equals `successChance`
- **Difficulty affects magnitude**: Easy challenges (high successChance) produce high success scores but mild failure scores. Hard challenges (low successChance) produce mild success scores but severe failure scores.
- **Why this matters**: When combining multiple skill checks, harder challenges contribute more negative scores on failure. This means a novice attempting something beyond their skill will be consistently penalized, while masters rarely suffer catastrophic failures on easy tasks.
- **Why logarithm?** Converts multiplicative relationships to additive ones, and produces unbounded but increasingly rare extreme values.
- **Why factor of 10?** Scales to human-friendly numbers. A "10" means you rolled an order of magnitude better than needed.

**Examples with successChance = 0.9 (easy check):**
| random | ratio | score |
|--------|-------|-------|
| 0.09 | 0.1 | +10 (great success) |
| 0.45 | 0.5 | +3 (good success) |
| 0.81 | 0.9 | +0.5 (marginal success) |
| 0.99 | 1.1 | -0.5 (mild failure) |

**Examples with successChance = 0.1 (hard check):**
| random | ratio | score |
|--------|-------|-------|
| 0.01 | 0.1 | +10 (great success) |
| 0.05 | 0.5 | +3 (good success) |
| 0.50 | 5.0 | -7 (significant failure) |
| 0.99 | 9.9 | -10 (severe failure) |

### Step 6: Update Skill State

After the check:

```go
skill.LastBase = rechargeCoeff      // Remember fatigue level
skill.LastUsedAt = now              // Mark usage time
```

If learning is enabled (`improve = true`), the skill also gains experience (see Skill Improvement section).

## Multi-Skill Challenges

When an action requires multiple skills, results are **summed**:

```go
func (c Challenges) Check(challenger *Object, targetID string) float64 {
    result := 0.0
    for _, challenge := range c {
        result += challenge.Check(challenger, targetID)
    }
    return result
}
```

Overall success if `result > 0`.

**How the unified formula affects multi-skill challenges:**

With the unified formula, the score magnitude depends on the difficulty:
- Easy challenges (high successChance) contribute positive expected scores
- Hard challenges (low successChance) contribute negative expected scores
- A single 50% check still yields 50% overall success

**Statistical properties:**
| Combination | Success Rate | Reason |
|-------------|-------------|--------|
| Two 50% checks | ~60% | Slight positive bias when summed |
| Easy (90%) + Hard (10%) | ~29% | Hard challenge penalty dominates |
| Two easy (90% each) | ~98% | Both contribute positive scores |
| Two hard (10% each) | ~5% | Both contribute negative scores |

**Why this design works for gameplay:**

1. **Skill match matters**: Attempting tasks beyond your skill level consistently hurts your overall score. You can't rely on luck to overcome a fundamental skill gap.

2. **Masters excel at easy tasks**: A highly skilled character almost never fails catastrophically at something within their ability. Even unlucky rolls produce mild failures.

3. **Novices struggle with hard tasks**: Even lucky rolls don't fully compensate for attempting something far beyond your skill. The severe failure penalty on hard challenges ensures consistent underperformance.

4. **Quality preservation**: The final sum still indicates overall quality. A barely-positive sum suggests scraping by; a large positive sum indicates masterful execution.

## Duration (Deterministic Windows)

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

## Skill Improvement

When `improve = true`, skills grow through use:

```go
improvement = rechargeCoeff * skillCoeff * theoryCoeff * challengeCoeff * perUse
skill.Practical += improvement
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

```
challengeCoeff = 1 / (1 + |challenge.Level - effective|)
```

- **Range**: (0, 1]
- **At challenge.Level = effective**: `challengeCoeff = 1` (optimal learning)
- **At |difference| = 1**: `challengeCoeff = 0.5`
- **At |difference| = 9**: `challengeCoeff = 0.1`
- **Why?** You learn best from appropriate challenges. Too easy = no learning ("I already knew that"). Too hard = no learning ("I have no idea what just happened"). The sweet spot is challenges that match your level.

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

1. Each description has `[]Challenge` requirements
2. Viewer's skills are checked against all challenges (summed)
3. If sum > 0, that description is visible
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

## Design Rationale

1. **Logical consistency**: Deterministic RNG within time windows means skill checks (like hiding or perception) remain valid for a duration rather than flickering with each command.

2. **Meaningful choices**: Recharge times make skill use a resource to manage, not spam.

3. **Natural learning curve**: Diminishing returns and appropriate-challenge bonuses encourage organic progression.

4. **Graceful multi-skill handling**: Summing log-odds preserves statistical properties while allowing compensation between skills.

5. **Quality over binary**: Knowing *how well* you succeeded enables richer game mechanics than simple pass/fail.
