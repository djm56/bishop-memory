# docs/

Build material only: what is still to do and the design plans for doing it.

User and developer documentation is the wiki. Its source is `docs/wiki/`,
published to <https://github.com/djm56/bishop-memory/wiki> with
`make wiki-publish`. Edit the pages there, not on github.com. When a plan
ships, document the result in the wiki and delete the plan; git history keeps
it.

| Path | What it is |
|---|---|
| [ROADMAP.md](ROADMAP.md) | Every outstanding item and future piece of work, with its status and plan |
| [plans/NETWORK-DEPLOYMENT-PLAN.md](plans/NETWORK-DEPLOYMENT-PLAN.md) | Install on any Unix server, network access, API keys |
| [plans/POSTGRES-PLAN.md](plans/POSTGRES-PLAN.md) | An optional PostgreSQL backend beside SQLite |
| [plans/MISSION-HUD-PLAN.md](plans/MISSION-HUD-PLAN.md) | The mission HUD; phases 1 and 3 shipped, phase 2 (structured debriefs) deferred |
| [wiki/](wiki/) | Source of the GitHub wiki |
