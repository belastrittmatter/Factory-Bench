# FactoryBench

FactoryBench is a lightweight metric logging tool for software factories: pipelines where AI agents turn GitHub issues into PRs. It records what every attempt cost, how long it took, how much a human had to step in, which tools the agents used and what happened to the PR, so you can spot trends and improve your factory over time. The same data also compares factories or configurations head to head (`report factories`, `--by config`).

What it measures:

- **Cost and tokens** per attempt, issue and merged issue, priced from tokens or as the provider reported it.
- **Time**: elapsed, active and human wait; time to first PR and to merge.
- **Human effort**: waits, steering, human commits and change requests on agent PRs.
- **Tool use**: tool calls, failures, subagents, API errors.
- **PR outcomes**: merged or rejected, review rounds, CI on first push, PR size, escaped defects.

How it works: a factory sends a few lifecycle events over HTTP (attempt started, PR linked, attempt finished; plus token usage for agents other than Claude Code). Claude Code sends its own telemetry (tokens, cost, tool calls, subagents) over OTLP. FactoryBench polls GitHub for PRs, reviews, commits and CI, and computes every metric. Reporting never blocks a factory.

![Architecture](docs/diagrams/architecture.svg)

## Quick start

```bash
go build -o factorybench ./cmd/factorybench          # Go 1.27+

# operator: register a factory (prints its API token once), then run the server
./factorybench factory add my-factory --commit-email factory-bot@users.noreply.github.com --github-login factory-bot
./factorybench serve --addr :8080 --poll 3m

# factory: send events (always exits 0, spools on failure)
export FACTORYBENCH_URL=http://localhost:8080 FACTORYBENCH_TOKEN=<token>
./factorybench emit attempt.started --attempt a1 --key started \
  --json '{"work_item":{"repo":"owner/product-repo","issue":1},"factory_config_version":"v1"}'

# reports
./factorybench report live
./factorybench report items
./factorybench report factories

# dashboard: read-only, never writes to or migrates the database, safe next to `serve`
./factorybench dashboard --db fb.db --addr 127.0.0.1:8081
```

The dashboard (Overview, Live, Issues, issue and attempt detail with lane flow, Compare; see [docs/dashboard.md](docs/dashboard.md)) uses the same report code as the CLI. It binds to loopback by default; `--addr :8090` shares it on the network without a login, so use it on a trusted network only. Two optional local views read files on request and are served only on a loopback address: `--runs <super-board runs dir>` (run manifests, worker logs, lane flow) and `--transcripts <Claude project dir>` (transcripts, matched through the `session.id` on the attempt's OTLP events). They contain prompts and code and are never stored. Tool inputs are never shown, only their argument names and sizes. A running attempt with no event for `--idle` (default 10m) shows as idle; lanes, board states and dispatch outcomes are read from the manifests as they are written, with no list of lane names built in.

## Docs

- **[INSTALL.md](INSTALL.md)**: integrate a factory (written so a coding agent can follow it).
- [docs/reference.md](docs/reference.md): concepts, contract rules, metric definitions, storage.
- [spec/events.schema.json](spec/events.schema.json): the event contract, with one example per type in [spec/examples/](spec/examples/).
- [docs/diagrams/](docs/diagrams/): issue flow, event path, attempt states, DB schema, code map; regenerate with `python3 docs/diagrams/generate.py`.
