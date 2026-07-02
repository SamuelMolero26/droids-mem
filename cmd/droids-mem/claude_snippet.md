## droids-mem session memory

This environment records a memory at the end of each Claude Code session.

- **Stage a session summary** when the run did something worth recalling next
  time — a fix, a decision, a non-obvious discovery, an established convention.
  When the Stop hook asks you to record progress, compose and stage:

  ```
  droids-mem session stage --session <session_id> \
    --title "<one-line imperative summary>" \
    --what "<what happened / what was attempted>" \
    --learned "<the reusable insight to apply next time>"
  ```

- **Decline for low-value runs.** A quick question, a trivial edit, or a run that
  went nowhere is not worth a summary. If nothing is worth recalling, do not
  stage — the gate already drops sessions below the change threshold, and your
  judgment is the second filter.

- **One summary per session.** Re-stage (same command) to update it as the run
  progresses; only the latest staged version is flushed at session end.

- **No secrets.** The store scrubs on save, but keep tokens/keys out of the
  summary text anyway.

- **`task_type`** defaults to `claude_session`. Pass `--task-type <slug>` only
  when the whole run had one clear workflow theme.
