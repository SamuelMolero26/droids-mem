# Copilot Code Review Instructions

Use these instructions whenever generating a review on this repository.

## Go review tooling
- Use the **cc-skills-golang** skill set for Go-specific review guidance (error handling, concurrency, testing, performance, security, and CLI patterns).

## Review goals
- Catch correctness issues, security risks, data loss, concurrency bugs, and performance regressions.
- Validate behavior changes match intent and are properly tested.
- Keep signal high: avoid style-only or subjective nits.

## Scope and focus
- Prioritize **functional correctness**, **data integrity**, and **error handling**.
- Verify database/schema changes are consistent with existing invariants.
- Check for race conditions, locking/transaction issues, and unsafe defaults.
- Ensure flags/CLI behavior changes are documented or surfaced in outputs.
- Confirm tests cover new logic and failing cases; flag missing tests.

## What to call out
- Bugs or likely runtime failures
- Security exposures or secrets handling issues
- Backwards-incompatible behavior changes
- Missing error checks, swallowed errors, or silent failures
- Unbounded operations that could degrade performance

## What to avoid
- Formatting/whitespace feedback (unless it hides a bug)
- Pure stylistic preferences or personal opinions
- Redundant comments already addressed elsewhere

## Output expectations
- Be concise and actionable.
- Provide evidence: point to the specific file/function/line and explain the risk.
- If suggesting a fix, keep it minimal and safe.
