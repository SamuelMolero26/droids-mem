## Future Implementations to consider

-  [ ] Implement TOON instead of JSON for better performance and smaller size. For save.go and context.go. Makes sense for context.go since is going into agent context.

-  [ ] Need to implement encoder for GO to handle GO serialization.

-  [ ] **Lazy loading patterns refinement**: formalize agent-driven `get`/`search` chains after `context` browse-tier. Track which browse-tier IDs agents most often expand to inform future tier sizing.

-  [ ] **Mode presets for `context`**: add `--mode orient|deep|refresh` flag layered on the always/browse tier model.
    - `orient` (default) = current always-tier + browse-tier (titles+1-liner for errors/patterns).
    - `deep` = always-tier + top-K errors/patterns full body, ranked by `--query`.
    - `refresh` = always-tier only (latest summary + user_rules), cheap mid-run check-in.
    Discoverable via `droids-mem schema context`.

-  [ ] **Auto-compaction of related memories**: detect superseding user_rules (newer rule contradicts older) and either merge or mark older as `superseded_by`. Reduces noise in always-tier when rules evolve.

-  [ ] **PII scrub regex patterns**: extend `scrubPII()` stub with patterns for email, phone (E.164), credit cards (Luhn-checked), API keys (`sk_*`, `pk_*`, `AKIA*`, `ghp_*`), JWTs, IP addresses. Tune false-positive rate against real corpus.

-  [ ] **TTL/retention beyond session_summary cap**: optional age-based or count-based expiry per Kind. Today only `session_summary` has a 5-per-task_type cap.