# Concepts

These are proposals, not documentation. Everything under [`docs/`](../) one level
up describes what the tool **does**; everything in this folder describes what it
**could do** about a requirement, and none of it is implemented yet.

Each concept opens with a status line saying as much, weighs options against
stated criteria, and keeps any claim about platform behaviour that has not been
seen working in a "to verify" section rather than in the prose as fact. When a
concept is built, its concept file is replaced by real documentation up in
`docs/` and removed from here.

| | |
| --- | --- |
| [credentials.md](credentials.md) | Giving migrated devices their new credentials (requirement #3). **Option A is built** and documented up in `docs/`; B and C remain proposals. |
| [credentials-verification.md](credentials-verification.md) | The procedures that settle the assumptions `credentials.md` rests on. Three of them can sink the option they belong to, so they gate the build. |
| [remote-targets.md](remote-targets.md) | Migrating to a tenant this instance is not subscribed to — different parent, or a different system. Option 4 (certificate exchange) chosen, verified, and now being built. |
