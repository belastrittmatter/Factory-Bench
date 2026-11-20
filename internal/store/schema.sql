-- registered factories; the API token identifies the factory
CREATE TABLE factories (
  factory_id   TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL,
  commit_email TEXT,                       -- commit author identity, for factory vs human commits
  github_login TEXT,                       -- GitHub account the factory acts as (closes issues, reviews, commits)
  created_at   TEXT NOT NULL
);

-- source of truth: everything that arrived, untouched, append-only
CREATE TABLE events (
  source       TEXT NOT NULL CHECK (source IN ('contract', 'github', 'otlp')),
  factory_id   TEXT NOT NULL DEFAULT '',   -- '' for GitHub events
  id           TEXT NOT NULL,              -- idempotency key (contract id, GitHub delivery id, or OTLP session.id:event.sequence:event.timestamp)
  type         TEXT NOT NULL,
  ts           TEXT NOT NULL,              -- when it happened
  received_at  TEXT NOT NULL,              -- when FactoryBench got it
  payload      TEXT NOT NULL,              -- raw JSON as received
  rejected     TEXT,                       -- validation error, if any
  PRIMARY KEY (source, factory_id, id)
);

-- everything below is derived and rebuilt by `factorybench replay`
CREATE TABLE work_items (
  repo         TEXT NOT NULL,
  issue        INTEGER NOT NULL,
  task_type    TEXT,                       -- from the issue's labels (else the factory's), e.g. bug, perf, chore
  title        TEXT,                       -- from the GitHub issue, NULL until reconciled
  issue_state  TEXT,                       -- open | closed
  closed_at    TEXT,
  closed_by    TEXT,                       -- GitHub login; a factory's github_login = closed by the agent
  first_seen   TEXT NOT NULL,              -- cohort date
  outcome      TEXT,                       -- open | merged | rejected | reworked | reverted
  survival     TEXT,                       -- pending | survived | not_survived | NULL
  corrects     TEXT,                       -- 'repo#issue' this item corrects, if any
  PRIMARY KEY (repo, issue)
);

CREATE TABLE attempts (
  factory_id        TEXT NOT NULL,
  attempt_id        TEXT NOT NULL,
  repo              TEXT NOT NULL,
  issue             INTEGER NOT NULL,
  parent_attempt_id TEXT,
  role              TEXT,                  -- plan | build | review | ...
  config_version    TEXT NOT NULL,
  state             TEXT NOT NULL CHECK (state IN
                      ('running', 'waiting_for_human', 'completed', 'failed', 'cancelled')),
  started_at        TEXT NOT NULL,
  ended_at          TEXT,
  human_wait_ms     INTEGER NOT NULL DEFAULT 0,
  pr_url            TEXT,
  waiting_since     TEXT,                  -- start of the open human wait, NULL if none (last column: v1 -> v2 ALTER)
  PRIMARY KEY (factory_id, attempt_id)
);

CREATE TABLE attempt_labels (               -- the labels of attempt.started, one row each (--label filters, max-turns: curve)
  factory_id        TEXT NOT NULL,
  attempt_id        TEXT NOT NULL,
  label             TEXT NOT NULL,
  PRIMARY KEY (factory_id, attempt_id, label)
);

CREATE TABLE usage (                        -- one row per usage event
  factory_id        TEXT NOT NULL,
  attempt_id        TEXT NOT NULL,
  model             TEXT NOT NULL,
  ts                TEXT NOT NULL,
  input             INTEGER NOT NULL,
  output            INTEGER NOT NULL,
  cache_read        INTEGER,                -- NULL = not reported, never 0 by default
  cache_write       INTEGER,
  reasoning         INTEGER,
  duration_ms       INTEGER,
  cost_usd          REAL,                   -- NULL when it can't be priced
  cost_provenance   TEXT CHECK (cost_provenance IN ('reported', 'estimated')),
  price_version     TEXT,
  phase             TEXT,                   -- implement | verify | review, NULL if not sent
  agent_id          TEXT,                   -- subagent, NULL = main agent (v2 -> v3 ALTER)
  step              TEXT                    -- worker step as the factory names it (factorybench.step), NULL if not sent (last column: v3 -> v4 ALTER)
);

CREATE TABLE tool_calls (                   -- OTLP tool_result, or a contract activity with a tool
  factory_id   TEXT NOT NULL,
  attempt_id   TEXT NOT NULL,
  agent_id     TEXT,                        -- NULL = main agent
  ts           TEXT NOT NULL,
  tool_name    TEXT NOT NULL,
  success      INTEGER,                     -- 1 | 0, NULL if not reported
  duration_ms  INTEGER,
  error_type   TEXT                         -- e.g. ShellError
);

CREATE TABLE agent_runs (                   -- one row per subagent seen on any event
  factory_id       TEXT NOT NULL,
  attempt_id       TEXT NOT NULL,
  agent_id         TEXT NOT NULL,
  parent_agent_id  TEXT,                    -- NULL = child of the main agent
  agent_type       TEXT,                    -- e.g. general-purpose
  is_async         INTEGER,
  model            TEXT,
  final_model      TEXT,
  tool_calls       INTEGER NOT NULL,        -- rows in tool_calls for this agent
  duration_ms      INTEGER,
  status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'killed')),
  started_at       TEXT NOT NULL,           -- its first event
  PRIMARY KEY (factory_id, attempt_id, agent_id)
);

CREATE TABLE api_errors (                   -- OTLP api_error / api_retries_exhausted
  factory_id   TEXT NOT NULL,
  attempt_id   TEXT NOT NULL,
  agent_id     TEXT,
  ts           TEXT NOT NULL,
  kind         TEXT NOT NULL,               -- api_error | api_retries_exhausted
  model        TEXT,
  status_code  INTEGER,
  error        TEXT,
  duration_ms  INTEGER
);

CREATE TABLE gates (                        -- factory-internal gate results (gate events)
  factory_id   TEXT NOT NULL,
  attempt_id   TEXT NOT NULL,
  ts           TEXT NOT NULL,
  name         TEXT NOT NULL,               -- e.g. build, guard, test
  passed       INTEGER NOT NULL,            -- 1 | 0
  reason       TEXT
);

CREATE TABLE context_turns (                -- one row per context.turn event; a later event for the same (attempt, n) replaces it
  factory_id       TEXT NOT NULL,
  attempt_id       TEXT NOT NULL,
  n                INTEGER NOT NULL,        -- turn number from 1, conversation order
  ts               TEXT NOT NULL,
  speaker          TEXT NOT NULL CHECK (speaker IN ('factory', 'person')),
  move             TEXT,                    -- factory turns: capture | mirror | show | suggest | expand | sharpen | challenge | narrow | ask | confirm
  words            INTEGER NOT NULL,
  dont_know        INTEGER NOT NULL,        -- 1 | 0, person tags
  could_not_answer INTEGER NOT NULL,
  already_said     INTEGER NOT NULL,
  in_attachments   INTEGER NOT NULL,
  suggestion       TEXT,                    -- accepted | rejected | none
  frustration      INTEGER,                 -- 0..5, NULL if not sent
  confirmed        INTEGER NOT NULL,
  revealed         TEXT NOT NULL,           -- JSON array of hidden-truth ids first stated by this turn
  tagged_by        TEXT,                    -- person | tagger | simulator
  text             TEXT,
  PRIMARY KEY (factory_id, attempt_id, n)
);

CREATE TABLE context_scores (               -- one row per Context creator attempt (context.score); a later event replaces it
  factory_id       TEXT NOT NULL,
  attempt_id       TEXT NOT NULL,
  ts               TEXT NOT NULL,
  scenario         TEXT NOT NULL,
  subject          TEXT,
  persona          TEXT,
  truth_items      INTEGER NOT NULL,
  truth_g4         INTEGER NOT NULL,
  truth_latent     INTEGER NOT NULL,
  truth_traps      INTEGER NOT NULL,
  truth_noise      INTEGER NOT NULL,
  full_items       INTEGER NOT NULL,        -- captured.full
  partial_items    INTEGER NOT NULL,
  g4_full          INTEGER NOT NULL,
  g4_partial       INTEGER NOT NULL,
  latent_drawn     INTEGER NOT NULL,
  latent_by_move   TEXT NOT NULL,           -- JSON object: move -> latent entries drawn after it
  traps_raised     INTEGER NOT NULL,
  traps_followed   INTEGER NOT NULL,
  invented         INTEGER NOT NULL,
  invented_unmarked INTEGER NOT NULL,
  map_tag_errors   INTEGER NOT NULL,
  assumed_marked   INTEGER NOT NULL,
  requirements     INTEGER NOT NULL,
  untraced         INTEGER NOT NULL,
  noise_left       INTEGER NOT NULL,
  missing_slots    INTEGER NOT NULL,
  contradictions   INTEGER NOT NULL,
  builders_agree   INTEGER NOT NULL,
  builders_total   INTEGER NOT NULL,
  vague_warnings   INTEGER NOT NULL,
  sim_violations   INTEGER NOT NULL,
  cost_person      REAL NOT NULL,           -- what the benchmark spent, USD; never part of the factory's cost
  cost_judge       REAL NOT NULL,
  cost_builders    REAL NOT NULL,
  cost_build       REAL NOT NULL,
  built            INTEGER NOT NULL,        -- 1 if the run was built (the build_* columns are then set)
  hidden_pass      INTEGER,
  hidden_total     INTEGER,
  build_tasks      INTEGER,
  build_merged     INTEGER,
  build_escalations INTEGER,
  build_repairs    INTEGER,
  PRIMARY KEY (factory_id, attempt_id)
);

CREATE TABLE prs (
  repo             TEXT NOT NULL,
  number           INTEGER NOT NULL,
  url              TEXT NOT NULL UNIQUE,
  branch           TEXT,
  state            TEXT,                    -- open | closed | merged
  merged_at        TEXT,
  factory_commits  INTEGER NOT NULL DEFAULT 0,
  human_commits    INTEGER NOT NULL DEFAULT 0,
  additions        INTEGER,                 -- PR size, NULL until fetched (last three columns: v2 -> v3 ALTER)
  deletions        INTEGER,
  changed_files    INTEGER,
  PRIMARY KEY (repo, number)
);

CREATE TABLE checks (
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  name          TEXT NOT NULL,              -- e.g. build, test, lint
  head_sha      TEXT NOT NULL,
  conclusion    TEXT,                       -- success | failure | ...
  started_at    TEXT,
  completed_at  TEXT
);

CREATE TABLE reviews (                      -- from the latest pr.reviews event of each PR
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  reviewer      TEXT NOT NULL,              -- GitHub login
  state         TEXT NOT NULL,              -- APPROVED | CHANGES_REQUESTED | COMMENTED | ...
  submitted_at  TEXT
);

CREATE TABLE pr_commits (                   -- from the latest pr.commits event of each PR
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  sha           TEXT NOT NULL,
  author_email  TEXT,
  factory_id    TEXT,                       -- NULL = human commit
  committed_at  TEXT                        -- commit author date
);
