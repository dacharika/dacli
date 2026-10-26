# Model economics and diversity

`team assign` selects the cheapest eligible role by estimated complexity (Te), declared capability tags, context limit, and cost tier. It is a floor, not an authority: uplift to a stronger role for security, data loss, architectural ambiguity, unfamiliar code, or irreversible consequence. Decompose work that exceeds capacity instead of treating a larger context as a safety substitute.

Before spend, use `runtime doctor`, `preflight`, and `spawn --advise`; bound a run with `--max-tokens`. Treat provider quota, authentication, rate limits, and runtime health as live facts. Shipped per-runtime health cooldown state persists so a restart does not immediately hammer a sick provider; broader service-level repeated-failure circuit breakers remain future work. Named fallback chains are opt-in; never silently substitute an explicit runtime/model. For adversarial review, use a genuinely different runtime or model family when independence matters.
