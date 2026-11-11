"""Generates the FactoryBench diagrams as standalone SVGs (light + dark aware).

Run: python3 docs/diagrams/generate.py
Writes architecture, issue-flow, db-schema, code-map, event-path and attempt-states SVGs next to this file.
"""
import html
import os

E = html.escape
HERE = os.path.dirname(os.path.abspath(__file__))

STYLE = """
svg{--foreground:#1d1d1f;--muted:#5f6368;--background:#ffffff;--card:#f4f4f5;--border:#c9c9cf;
--chart-1:#3b6fe0;--chart-2:#17a2a2;--chart-3:#d99a06;--chart-4:#8b5cf6;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
@media (prefers-color-scheme: dark){svg{--foreground:#ececef;--muted:#a1a1aa;--background:#0d1117;--card:#1b1f27;--border:#3a3f4b}}
.bg{fill:var(--background)}
text{fill:var(--foreground)}
.b{font-size:12px;font-weight:600}.s{font-size:10.5px;fill:var(--muted)}
.lb{font-size:10.5px;fill:var(--foreground);paint-order:stroke;stroke:var(--background);stroke-width:4px;stroke-linejoin:round}
.zt{font-size:11px;font-weight:600;fill:var(--muted);letter-spacing:.02em}
.zone{fill:none;stroke:var(--border);stroke-dasharray:3 3}
.ln{fill:none;stroke:var(--muted);stroke-width:1.3}.ds{stroke-dasharray:5 4}
.ahd{fill:var(--muted)}
.p0{fill:var(--card);stroke:var(--border)}
.p1{fill:color-mix(in srgb,var(--chart-1) 18%,var(--background));stroke:var(--chart-1)}
.p2{fill:color-mix(in srgb,var(--chart-2) 18%,var(--background));stroke:var(--chart-2)}
.p3{fill:color-mix(in srgb,var(--chart-3) 18%,var(--background));stroke:var(--chart-3)}
.p4{fill:color-mix(in srgb,var(--chart-4) 18%,var(--background));stroke:var(--chart-4)}
.life{stroke:var(--border);stroke-dasharray:2 3}
.grp{fill:none;stroke:var(--border)}.gl{stroke:var(--border);stroke-dasharray:4 3}
.gk{font-size:10.5px;font-weight:600;fill:var(--muted)}
.note{fill:color-mix(in srgb,var(--chart-4) 18%,var(--background));stroke:var(--chart-4)}
.th{font-size:12px;font-weight:700}.tc{font-size:10.5px;font-family:ui-monospace,Menlo,monospace}
.tt{font-size:10px;fill:var(--muted);font-family:ui-monospace,Menlo,monospace}
.pk{fill:var(--chart-3);font-size:10px;font-weight:700}.fk{fill:var(--chart-1);font-size:10px;font-weight:700}
.sep{stroke:var(--border)}
.lg{font-size:11px;fill:var(--muted)}
"""

DEFS = ('<defs><marker id="ah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        'orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" class="ahd"/></marker></defs>')


def box(x, y, w, h, lines, cls):
    s = f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="6" class="{cls}"/>'
    cy = y + h / 2 - (len(lines) - 1) * 7
    for i, l in enumerate(lines):
        s += f'<text x="{x + w / 2}" y="{cy + i * 14 + 4}" text-anchor="middle" class="{"b" if i == 0 else "s"}">{E(l)}</text>'
    return s


def line(pts, dash=False, arrow=True):
    d = "M" + " L".join(f"{x},{y}" for x, y in pts)
    m = ' marker-end="url(#ah)"' if arrow else ''
    return f'<path d="{d}" class="{"ln ds" if dash else "ln"}"{m}/>'


def lab(x, y, t, anchor="middle", rot=None):
    r = f' transform="rotate({rot} {x} {y})"' if rot else ''
    return f'<text x="{x}" y="{y}" text-anchor="{anchor}" class="lb"{r}>{E(t)}</text>'


def legend(y, items):
    """items: (class or None, text). Returns svg text and nothing else."""
    s, x = '', 8
    for cls, t in items:
        if cls:
            s += f'<rect x="{x}" y="{y - 9}" width="10" height="10" rx="2" class="{cls}"/>'
            x += 15
        s += f'<text x="{x}" y="{y}" class="lg">{E(t)}</text>'
        x += len(t) * 6.2 + 18
    return s


def svg(w, h, body, label):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" '
            f'aria-label="{E(label)}"><style>{STYLE}</style><rect class="bg" width="{w}" height="{h}"/>{DEFS}{body}</svg>\n')


# ---------------------------------------------------------------- architecture
def architecture():
    d = []
    d.append(legend(16, [('p1', 'People'), ('p2', 'Factory'), ('p3', 'Product CI (bench)'),
                         ('p4', 'FactoryBench'), (None, '── work / control'), (None, '- - - observation only')]))
    o = 30  # vertical offset below the legend
    for x, y, w, h, t in [(0, 100, 210, 300, 'Factory · e.g. plan → build loop'), (250, 100, 220, 405, 'Product repo · GitHub + CI'),
                          (510, 100, 210, 455, 'FactoryBench · passive')]:
        d.append(f'<rect x="{x}" y="{y + o}" width="{w}" height="{h}" rx="10" class="zone"/><text x="{x + 10}" y="{y + 18 + o}" class="zt">{E(t)}</text>')
    B = lambda x, y, w, h, l, c: box(x, y + o, w, h, l, c)
    L = lambda pts, **k: line([(x, y + o) for x, y in pts], **k)
    T = lambda x, y, t, *a, **k: lab(x, y + o, t, *a, **k)
    d.append(B(15, 15, 180, 52, ['Factory maintainers', 'one factory each'], 'p1'))
    d.append(B(265, 15, 190, 52, ['Humans', 'file issues, approve plans,', 'review, merge'], 'p1'))
    d.append(B(525, 15, 180, 52, ['FactoryBench operator', 'FactoryBench, bench/,', 'gate, nightly'], 'p1'))
    d.append(L([(105, 67), (105, 98)])); d.append(T(111, 86, 'own', 'start'))
    d.append(L([(360, 67), (360, 133)])); d.append(T(354, 88, 'issues, approve, merge', 'end'))
    d.append(L([(615, 67), (615, 98)])); d.append(T(621, 86, 'owns', 'start'))
    d.append(L([(540, 67), (540, 84), (440, 84), (440, 245)])); d.append(T(490, 79, 'owns bench/'))
    # factory
    d.append(B(15, 135, 180, 40, ['watch / run', 'claims labelled issues'], 'p2'))
    d.append(B(15, 195, 180, 70, ['loop: red → plan → green', 'gate ladder: build, guard,', 'test, bench budget (fast)'], 'p2'))
    d.append(B(60, 285, 135, 36, ['agy sessions'], 'p2'))
    d.append(B(15, 341, 180, 44, ['emitter', '~5 call sites, raw facts'], 'p2'))
    d.append(L([(105, 175), (105, 193)])); d.append(L([(128, 265), (128, 283)]))
    d.append(L([(35, 265), (35, 339)], dash=True))
    # product repo + CI
    d.append(B(265, 135, 150, 36, ['Issues + labels'], 'p0'))
    d.append(B(265, 191, 150, 36, ['Pull requests'], 'p0'))
    d.append(B(265, 247, 190, 62, ['Bench gate (CI)', 'main vs PR, same runner,', '5 runs each, median, budget'], 'p3'))
    d.append(B(300, 329, 155, 34, ['Check runs'], 'p0'))
    d.append(B(300, 383, 155, 40, ['Nightly 2 h soak', 'fake traffic, leak check'], 'p3'))
    d.append(B(265, 443, 190, 50, ['docs/health/ on main', 'bot commit, [skip ci]'], 'p0'))
    d.append(L([(265, 153), (197, 153)])); d.append(T(231, 147, 'poll'))
    d.append(L([(197, 214), (263, 214)])); d.append(T(231, 208, 'push, PR'))
    d.append(L([(340, 227), (340, 245)]))
    d.append(L([(380, 309), (380, 327)]))
    d.append(L([(380, 423), (380, 441)]))
    d.append(L([(280, 309), (280, 441)])); d.append(T(275, 380, 'on merge', rot=-90))
    d.append(L([(415, 262), (462, 262), (462, 153), (417, 153)])); d.append(T(458, 210, 'fail → perf issue', rot=-90))
    # FactoryBench
    d.append(B(525, 135, 180, 46, ['GitHub poller', 'API, every 2–5 min'], 'p4'))
    d.append(B(525, 211, 180, 34, ['raw events (kept)'], 'p4'))
    d.append(B(525, 265, 180, 40, ['normalize', 'pure, replayable'], 'p4'))
    d.append(B(525, 325, 180, 34, ['SQLite: items, attempts'], 'p4'))
    d.append(B(525, 379, 180, 50, ['reports', 'live · item · trends · survival'], 'p4'))
    d.append(B(525, 505, 180, 36, ['POST /v1/events'], 'p4'))
    d.append(L([(472, 158), (523, 158)], dash=True)); d.append(T(497, 150, 'API poll'))
    for a, b in [(181, 209), (245, 263), (305, 323), (359, 377)]:
        d.append(L([(615, a), (615, b)]))
    d.append(L([(705, 523), (714, 523), (714, 228), (707, 228)]))
    d.append(L([(105, 385), (105, 523), (523, 523)], dash=True))
    d.append(T(300, 517, 'contract events (only factory → FactoryBench link)'))
    return svg(722, 560 + o, ''.join(d), 'FactoryBench architecture: who handles what')


# ---------------------------------------------------------------- issue flow
def sequence(P, rows, label, W=722):
    """Sequence diagram. P: (key, title, class). rows: m(sender, receiver, text, dashed) | self | gs/gm/ge group | note."""
    gap = (W - 110) // (len(P) - 1)
    X = {k: 55 + i * gap for i, (k, _, _) in enumerate(P)}
    bw = min(108, gap - 8)
    body, y, step, stack = [], 50, 26, []
    for r in rows:
        k = r[0]
        if k == 'm':
            _, a, b, t, obs = r
            y += step
            x1, x2 = X[a], X[b]
            dirn = 1 if x2 > x1 else -1
            body.append(line([(x1, y), (x2 - dirn * 2, y)], dash=obs))
            body.append(lab((x1 + x2) / 2, y - 5, t))
        elif k == 'self':
            _, a, t = r
            y += step + 6
            x = X[a]
            if x < W - 222:
                body.append(line([(x, y - 10), (x + 26, y - 10), (x + 26, y + 4), (x + 3, y + 4)]))
                body.append(lab(x + 32, y - 1, t, 'start'))
            else:
                body.append(line([(x, y - 10), (x - 26, y - 10), (x - 26, y + 4), (x - 3, y + 4)]))
                body.append(lab(x - 32, y - 1, t, 'end'))
        elif k == 'gs':
            y += step
            stack.append((y - 14, r[1], r[2]))
        elif k == 'gm':
            y += step
            body.append(f'<line x1="22" x2="{W - 22}" y1="{y - 14}" y2="{y - 14}" class="gl"/><text x="30" y="{y - 1}" class="gk">[{E(r[1])}]</text>')
        elif k == 'ge':
            y += 12
            y0, kind, cond = stack.pop()
            body.insert(0, f'<rect x="22" y="{y0}" width="{W - 44}" height="{y - y0}" rx="4" class="grp"/>'
                           f'<text x="30" y="{y0 + 13}" class="gk">{kind} [{E(cond)}]</text>')
        elif k == 'note':
            y += step + 8
            x = X[r[1]]
            body.append(f'<rect x="{x - 110}" y="{y - 14}" width="165" height="22" rx="4" class="note"/>' + lab(x - 27, y + 1, r[2]))
    H = y + 30
    head = []
    for k, t, c in P:
        x = X[k]
        head.append(f'<line x1="{x}" x2="{x}" y1="38" y2="{H - 6}" class="life"/>'
                    f'<rect x="{x - bw // 2}" y="8" width="{bw}" height="30" rx="6" class="{c}"/>'
                    f'<text x="{x}" y="27" text-anchor="middle" class="b">{E(t)}</text>')
    return svg(W, H, ''.join(head + body), label)


def issue_flow():
    P = [('Dev', 'Engineer', 'p1'), ('GH', 'GitHub', 'p0'), ('F', 'Factory loop', 'p2'), ('AGY', 'agy', 'p2'),
         ('CI', 'Bench gate (CI)', 'p3'), ('FB', 'FactoryBench', 'p4')]
    rows = [
        ('m', 'Dev', 'GH', 'label issue agent:ready', False),
        ('m', 'F', 'GH', 'poll, claim (agent:working)', False),
        ('m', 'F', 'FB', 'attempt.started (role: plan)', True),
        ('m', 'F', 'AGY', 'red phase: write failing tests', False),
        ('m', 'F', 'FB', 'usage (per session)', True),
        ('m', 'F', 'GH', 'push, draft PR, agent:plan-review', False),
        ('m', 'F', 'FB', 'pr.linked · human.waiting · attempt.finished', True),
        ('m', 'GH', 'FB', 'poll: PR opened, labels', True),
        ('m', 'Dev', 'GH', 'label agent:plan-approved', False),
        ('m', 'F', 'FB', 'human.resumed · attempt.started (build, parent=plan)', True),
        ('gs', 'loop', 'up to maxAttempts'),
        ('m', 'F', 'AGY', 'green phase', False),
        ('m', 'F', 'FB', 'usage · activity', True),
        ('self', 'F', 'gates: build, guard, test, bench budget'),
        ('ge',),
        ('m', 'F', 'GH', 'push, PR ready (or draft + agent:needs-human)', False),
        ('m', 'F', 'FB', 'pr.linked · attempt.finished', True),
        ('m', 'GH', 'CI', 'PR updated', False),
        ('self', 'CI', 'main vs PR, 5 runs each, medians'),
        ('m', 'CI', 'GH', 'check run: pass / fail', False),
        ('m', 'GH', 'FB', 'poll: check runs', True),
        ('gs', 'alt', 'inside budget'),
        ('m', 'Dev', 'GH', 'review, merge', False),
        ('m', 'GH', 'FB', 'poll: merged, commit authors', True),
        ('m', 'CI', 'GH', 'bot commits docs/health to main [skip ci]', False),
        ('gm', 'breaks budget'),
        ('m', 'CI', 'GH', 'comment "idle mem 312 MB > 250 MB (main 205)" + perf issue', False),
        ('m', 'GH', 'FB', 'new work item, linked as corrective', True),
        ('ge',),
        ('note', 'FB', 'day 14: reconciler → survival'),
    ]
    return sequence(P, rows, 'One issue through a plan → build factory, and what FactoryBench sees')


# ---------------------------------------------------------------- db schema
TABLES = {
    # name: (x, y, width, class, [(column, type, key)])  key: PK, FK or ''
    'factories': (8, 40, 200, 'p0', [('factory_id', 'text', 'PK'), ('token_hash', 'text', ''), ('commit_email', 'text', ''),
                                     ('github_login', 'text, null', ''), ('created_at', 'text', '')]),
    'events': (8, 200, 200, 'p4', [('source', 'contract|github', 'PK'), ('factory_id', 'text', 'PK'), ('id', 'text', 'PK'),
                                   ('type', 'text', ''), ('ts', 'text', ''), ('received_at', 'text', ''),
                                   ('payload', 'json (raw)', ''), ('rejected', 'text, null', '')]),
    'work_items': (261, 40, 200, 'p0', [('repo', 'text', 'PK'), ('issue', 'int', 'PK'), ('task_type', 'text', ''),
                                        ('title', 'text', ''), ('issue_state', 'open|closed', ''), ('closed_at', 'text', ''),
                                        ('closed_by', 'login', ''), ('first_seen', 'text', ''), ('outcome', 'text', ''),
                                        ('survival', 'pending|…', ''), ('corrects', 'repo#issue', 'FK')]),
    'attempts': (261, 252, 200, 'p0', [('factory_id', 'text', 'PK'), ('attempt_id', 'text', 'PK'), ('repo, issue', '', 'FK'),
                                       ('parent_attempt_id', 'text', 'FK'), ('role', 'text', ''), ('config_version', 'text', ''),
                                       ('state', 'enum', ''), ('started_at', 'text', ''), ('ended_at', 'text', ''),
                                       ('human_wait_ms', 'int', ''), ('pr_url', 'text', 'FK')]),
    'usage': (261, 464, 200, 'p0', [('factory_id, attempt_id', '', 'FK'), ('model', 'text', ''), ('ts', 'text', ''),
                                    ('input / output', 'int', ''), ('cache_read / write', 'int', ''), ('reasoning', 'int', ''),
                                    ('cost_usd', 'real, null', ''), ('cost_provenance', 'text', ''),
                                    ('price_version', 'text', 'FK'), ('phase', 'impl|verify|review', '')]),
    'gates': (261, 664, 200, 'p0', [('factory_id, attempt_id', '', 'FK'), ('ts', 'text', ''), ('name', 'text', ''),
                                    ('passed', 'bool', ''), ('reason', 'text', '')]),
    'prs': (514, 40, 200, 'p0', [('repo', 'text', 'PK'), ('number', 'int', 'PK'), ('url', 'text', ''), ('branch', 'text', ''),
                                 ('state', 'text', ''), ('merged_at', 'text', ''), ('factory_commits', 'int', ''),
                                 ('human_commits', 'int', '')]),
    'checks': (514, 210, 200, 'p0', [('repo, pr_number', '', 'FK'), ('name', 'text', ''), ('head_sha', 'text', ''),
                                     ('conclusion', 'text', ''), ('started_at', 'text', ''), ('completed_at', 'text', '')]),
    'reviews': (514, 345, 200, 'p0', [('repo, pr_number', '', 'FK'), ('reviewer', 'login', ''), ('state', 'text', ''),
                                      ('submitted_at', 'text', '')]),
    'pr_commits': (514, 465, 200, 'p0', [('repo, pr_number', '', 'FK'), ('sha', 'text', ''), ('author_email', 'text', ''),
                                         ('factory_id', 'null = human', 'FK'), ('committed_at', 'text', '')]),
    'prices': (514, 600, 200, 'p0', [('version', 'text', 'PK'), ('model', 'text', 'PK'), ('input_per_mtok', 'real', ''),
                                     ('output_per_mtok', 'real', ''), ('cache_read_per_mtok', 'real', '')]),
}
ROW, HEAD = 15, 24


def db_schema():
    d = [legend(16, [('p4', 'source of truth (append-only)'), ('p0', 'derived: rebuilt by replay'),
                     (None, 'PK primary key · FK reference')])]

    def anchor(t, col, side):
        x, y, w, _, cols = TABLES[t]
        i = [c[0] for c in cols].index(col)
        return (x if side == 'l' else x + w, y + HEAD + i * ROW + ROW / 2 + 1)

    for name, (x, y, w, cls, cols) in TABLES.items():
        h = HEAD + len(cols) * ROW + 6
        d.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="6" class="{cls}"/>')
        d.append(f'<text x="{x + 10}" y="{y + 16}" class="th">{name}</text><line x1="{x}" x2="{x + w}" y1="{y + HEAD - 2}" y2="{y + HEAD - 2}" class="sep"/>')
        for i, (c, t, k) in enumerate(cols):
            cy = y + HEAD + i * ROW + 11
            if k:
                d.append(f'<text x="{x + 8}" y="{cy}" class="{k.lower()}">{k}</text>')
            d.append(f'<text x="{x + 30}" y="{cy}" class="tc">{E(c)}</text>')
            if t:
                d.append(f'<text x="{x + w - 8}" y="{cy}" text-anchor="end" class="tt">{E(t)}</text>')

    def link(a, ac, b, bc, label=None, lx=None):
        (x1, y1), (x2, y2) = anchor(a, ac, 'r'), anchor(b, bc, 'l')
        mx = lx if lx is not None else (x1 + x2) / 2
        d.append(line([(x1, y1), (mx, y1), (mx, y2), (x2 - 2, y2)]))
        if label:
            d.append(lab(mx, min(y1, y2) - 4, label))

    def loop_left(t, c1, c2, off=14):
        (x, y1), (_, y2) = anchor(t, c1, 'l'), anchor(t, c2, 'l')
        d.append(line([(x, y1), (x - off, y1), (x - off, y2), (x - 2, y2)]))

    link('factories', 'factory_id', 'attempts', 'factory_id', lx=232)
    link('work_items', 'repo', 'prs', 'repo', 'issue ↔ PR', lx=488)
    loop_left('work_items', 'corrects', 'repo', off=8)
    loop_left('attempts', 'parent_attempt_id', 'attempt_id', off=8)
    (x1, y1), (x2, y2) = anchor('work_items', 'repo', 'l'), anchor('attempts', 'repo, issue', 'l')
    d.append(line([(x1, y1), (x1 - 17, y1), (x1 - 17, y2), (x2 - 2, y2)]))
    (x1, y1), (x2, y2) = anchor('attempts', 'attempt_id', 'l'), anchor('usage', 'factory_id, attempt_id', 'l')
    d.append(line([(x1, y1), (x1 - 38, y1), (x1 - 38, y2), (x2 - 2, y2)]))
    link('attempts', 'pr_url', 'prs', 'url', lx=488)
    for t in ('checks', 'reviews', 'pr_commits'):
        (x1, y1), (x2, y2) = anchor('prs', 'repo', 'r'), anchor(t, 'repo, pr_number', 'r')
        d.append(line([(x1, y1), (x1 + 6, y1), (x1 + 6, y2), (x2 + 2, y2)]))
    (x1, y1), (x2, y2) = anchor('attempts', 'attempt_id', 'l'), anchor('gates', 'factory_id, attempt_id', 'l')
    d.append(line([(x1, y1), (x1 - 38, y1), (x1 - 38, y2), (x2 - 2, y2)]))
    link('usage', 'price_version', 'prices', 'version', lx=488)
    # events feed everything derived
    ex, ey, ew, _, ecols = TABLES['events']
    eh = HEAD + len(ecols) * ROW + 6
    d.append(line([(ex + ew / 2, ey + eh), (ex + ew / 2, ey + eh + 50), (255, ey + eh + 50)], dash=True))
    d.append(lab(ex + ew / 2 + 4, ey + eh + 30, 'normalize / replay', 'start'))
    d.append(f'<text x="{ex}" y="{ey + eh + 90}" class="lg">Intake writes only to events.</text>'
             f'<text x="{ex}" y="{ey + eh + 105}" class="lg">factory_id comes from the token,</text>'
             f'<text x="{ex}" y="{ey + eh + 120}" class="lg">never from the request body.</text>')
    return svg(722, 780, ''.join(d), 'FactoryBench database schema')


# ---------------------------------------------------------------- code map
def code_map():
    W = 804
    C = [8 + i * 134 for i in range(6)]  # column x; boxes are 118 wide, 16 px gutters
    cx = [x + 59 for x in C]
    B = lambda c, y, h, l, cls, w=118: box(C[c], y, w, h, l, cls)
    d = [legend(16, [('p4', 'FactoryBench package'), ('p2', 'factory side'), ('p1', 'console'),
                     ('p0', 'data / external'), (None, '── call / data'), (None, '- - - read / callback')])]
    # main.go: dispatch only
    d.append(f'<rect x="8" y="34" width="{W - 16}" height="48" rx="6" class="p0"/>'
             f'<text x="18" y="51" class="b">cmd/factorybench/main.go</text>'
             f'<text x="190" y="51" class="s">subcommand dispatch only; each subcommand calls one package below</text>')
    for c, t in enumerate(['claude-translate', 'emit [--flush]', 'serve · reconcile', 'factory add → store',
                           'replay', 'report live|item|…']):
        d.append(f'<text x="{cx[c]}" y="72" text-anchor="middle" class="tc">{E(t)}</text>')
    # column 0: factory side + test harness
    d.append(B(0, 130, 54, ['Factory', 'any factory or', 'scripts/*.sh'], 'p2'))
    d.append(B(0, 222, 40, ['claude -p', 'stream-json'], 'p2'))
    d.append(B(0, 296, 54, ['claudecode', 'stream → usage +', 'finished payloads'], 'p4'))
    d.append(line([(cx[0], 184), (cx[0], 220)])); d.append(lab(cx[0] + 6, 205, 'runs', 'start'))
    d.append(line([(cx[0], 262), (cx[0], 294)]))
    d.append(line([(126, 330), (134, 330), (134, 172), (140, 172)]))
    d.append(line([(126, 148), (140, 148)]))
    # column 1: emit, spool, GitHub API
    d.append(B(1, 130, 54, ['emit', 'build envelope, POST,', 'never fails the caller'], 'p4'))
    d.append(B(1, 222, 40, ['spool.jsonl', '~/.factorybench/'], 'p0'))
    d.append(line([(cx[1] - 22, 184), (cx[1] - 22, 220)], dash=True)); d.append(lab(cx[1] - 26, 206, 'POST failed', 'end'))
    d.append(line([(cx[1] + 22, 222), (cx[1] + 22, 186)], dash=True)); d.append(lab(cx[1] + 26, 206, '--flush', 'start'))
    d.append(B(1, 318, 44, ['GitHub REST API', 'PRs, commits, checks'], 'p0'))
    # column 2: intake, github
    d.append(B(2, 130, 62, ['intake', 'POST /v1/events', 'auth · validate · insert'], 'p4'))
    d.append(line([(260, 148), (274, 148)]))
    d.append(B(2, 312, 56, ['github', 'Reconcile(): poll,', 'store as raw events'], 'p4'))
    d.append(line([(274, 340), (262, 340)]))
    # column 3: events
    d.append(B(3, 130, 72, ['events', 'raw, append-only,', 'source of truth,', 'PK dedupes resends'], 'p4'))
    d.append(line([(394, 160), (408, 160)]))
    d.append(line([(394, 326), (450, 326), (450, 204)])); d.append(lab(444, 270, 'INSERT', 'end'))
    d.append(line([(488, 204), (488, 354), (396, 354)], dash=True)); d.append(lab(400, 372, 'reads pr.linked URLs', 'start'))
    # column 4: normalize + derived tables
    d.append(B(4, 130, 72, ['normalize', 'derive(): pure', 'Run(): wipe + rebuild', 'in one transaction'], 'p4'))
    d.append(line([(528, 166), (542, 166)]))
    d.append(B(4, 248, 92, ['derived tables', 'work_items · attempts', 'usage · gates · prs', 'checks · reviews', 'pr_commits'], 'p0'))
    d.append(line([(cx[4], 202), (cx[4], 246)])); d.append(lab(cx[4] + 6, 228, 'INSERT', 'start'))
    # column 5: golden tests, report, stdout
    d.append(B(5, 130, 72, ['golden tests', 'testdata/golden/*', 'POST → normalize', '→ report = report.txt'], 'p0'))
    d.append(B(5, 248, 62, ['report', 'live · item', 'items · factories'], 'p4'))
    d.append(line([(662, 280), (676, 280)]))
    d.append(B(5, 350, 40, ['stdout', 'tabwriter tables'], 'p1'))
    d.append(line([(cx[5], 310), (cx[5], 348)]))
    # callback lane: intake -> normalize.Run after each batch (wired in main.go)
    d.append(line([(cx[2], 130), (cx[2], 106), (cx[4], 106), (cx[4], 128)], dash=True))
    d.append(lab(cx[3], 102, 'after(): normalize.Run once per batch (poll and reconcile call it too)'))
    # shared packages
    d.append(f'<rect x="4" y="414" width="{W - 8}" height="112" rx="10" class="zone"/><text x="14" y="431" class="zt">shared packages: imported, not a stage</text>')
    d.append(box(14, 440, 250, 76, ['contract', 'Event, Payload, Validate()', 'mirrors spec/events.schema.json', 'used by intake, emit, normalize'], 'p4'))
    d.append(box(277, 440, 250, 76, ['store', 'Open() runs schema.sql (embedded)', 'InsertEvent · AddFactory · FactoryByToken', 'used by main, intake, emit, github'], 'p4'))
    d.append(box(540, 440, 250, 76, ['pricing', 'Estimate() from the newest', 'prices/YYYY-MM-DD.json (embedded)', 'used by normalize'], 'p4'))
    return svg(W, 534, ''.join(d), 'FactoryBench Go packages and how they connect')


# ---------------------------------------------------------------- event path
def event_path():
    P = [('F', 'Factory (emit)', 'p2'), ('IN', 'intake', 'p4'), ('CT', 'contract', 'p4'), ('ST', 'store · SQLite', 'p0'),
         ('NM', 'normalize', 'p4'), ('RP', 'report', 'p4'), ('OUT', 'stdout', 'p1')]
    rows = [
        ('self', 'F', 'emit usage --attempt a1 --key usage:1 --json …'),
        ('self', 'F', 'envelope: spec, id = a1:usage:1, ts = now'),
        ('m', 'F', 'IN', 'POST /v1/events  [event]  Bearer fbt_…', False),
        ('m', 'IN', 'ST', 'FactoryByToken(sha256(token))', False),
        ('m', 'ST', 'IN', 'factory_id (never from the body)', True),
        ('gs', 'alt', 'unknown token · body not a JSON array · > 10 MB'),
        ('m', 'IN', 'F', '401 / 400', False),
        ('self', 'F', 'append to spool.jsonl, exit 0'),
        ('ge',),
        ('self', 'IN', 'mu.Lock(): one batch at a time'),
        ('gs', 'loop', 'each event in the batch'),
        ('m', 'IN', 'CT', 'Validate(raw)', False),
        ('m', 'CT', 'IN', 'Event, Payload, error', True),
        ('m', 'IN', 'ST', 'InsertEvent(contract, factory, id, type, ts, raw, rejected)', False),
        ('self', 'ST', 'ON CONFLICT: replace only a rejected row'),
        ('m', 'ST', 'IN', 'dup = 0 rows affected', True),
        ('ge',),
        ('gs', 'opt', 'accepted + rejected > 0'),
        ('m', 'IN', 'NM', 'after(): normalize.Run(db)', False),
        ('m', 'NM', 'ST', 'BEGIN · DELETE derived tables', False),
        ('m', 'NM', 'ST', 'SELECT accepted events ORDER BY ts', False),
        ('self', 'NM', 'derive(): attempts, waits, prices'),
        ('m', 'NM', 'ST', 'INSERT derived rows · COMMIT', False),
        ('ge',),
        ('m', 'IN', 'F', '200 {accepted, duplicates, rejected[]}', False),
        ('self', 'F', 'rejected? log it to stderr (kept in events)'),
        ('m', 'OUT', 'RP', '$ factorybench report live', False),
        ('m', 'RP', 'ST', 'SELECT attempts running | waiting + usage sums', False),
        ('m', 'ST', 'RP', 'rows', True),
        ('self', 'RP', 'elapsed, active = elapsed − wait, cost'),
        ('m', 'RP', 'OUT', 'tabwriter table', False),
    ]
    return sequence(P, rows, 'One event from factorybench emit to the console', W=804)


# ---------------------------------------------------------------- attempt states
def attempt_states():
    W = 804
    d = [legend(16, [('p4', 'attempts.state'), ('p3', 'finished, but a human still has to act'),
                     (None, 'dashed: wait closed by a later event')])]
    d.append('<g transform="translate(0,-55)">')  # state area starts high; the coordinates below read as written
    d.append(f'<circle cx="30" cy="125" r="7" class="ahd"/>')
    d.append(line([(37, 125), (98, 125)])); d.append(lab(67, 117, 'started'))
    d.append(box(100, 100, 190, 50, ['running', 'ended_at NULL'], 'p4'))
    d.append(box(510, 100, 190, 50, ['waiting_for_human', 'open wait since ts'], 'p4'))
    d.append(box(100, 300, 190, 54, ['completed | failed |', 'cancelled', 'terminal: ended_at set'], 'p4'))
    d.append(box(510, 300, 190, 54, ['waiting_for_human', 'ended_at set,', 'status parked'], 'p3'))
    d.append(line([(290, 115), (508, 115)])); d.append(lab(400, 109, 'human.waiting · opens the wait'))
    d.append(line([(510, 138), (292, 138)])); d.append(lab(400, 168, 'human.resumed · wait_ms += resumed − waiting'))
    d.append(line([(195, 150), (195, 298)])); d.append(lab(201, 220, 'attempt.finished', 'start')); d.append(lab(201, 234, '→ state = status', 'start'))
    d.append(line([(605, 150), (605, 298)])); d.append(lab(611, 220, 'attempt.finished', 'start')); d.append(lab(611, 234, 'while the wait is open', 'start'))
    d.append(line([(510, 327), (292, 327)], dash=True)); d.append(lab(400, 321, 'human.resumed on it or its child'))
    d.append(lab(400, 343, '→ takes the parked status'))
    d.append(f'<text x="100" y="376" class="lg">Events of a terminal attempt no longer change its state. Events for an attempt that never started stay in events only.</text>')

    # plan → build timeline (a plan → build factory)
    y0, t0, t1, x0, x1 = 410, 0, 22, 150, 780
    T = lambda m: x0 + (x1 - x0) * (m - t0) / (t1 - t0)
    d.append(f'<text x="8" y="{y0}" class="zt">plan → build: one wait, two attempts (minutes after 09:00)</text>')
    ax = y0 + 120
    d.append(f'<line x1="{x0}" x2="{x1}" y1="{ax}" y2="{ax}" class="sep"/>')
    for m in range(0, 23, 2):
        d.append(f'<line x1="{T(m)}" x2="{T(m)}" y1="{ax}" y2="{ax + 4}" class="sep"/><text x="{T(m)}" y="{ax + 16}" text-anchor="middle" class="tt">{m}</text>')

    def bar(row, a, b, cls, text):
        y = y0 + 22 + row * 44
        return (f'<rect x="{T(a)}" y="{y}" width="{T(b) - T(a)}" height="24" rx="4" class="{cls}"/>'
                f'<text x="{(T(a) + T(b)) / 2}" y="{y + 16}" text-anchor="middle" class="s">{E(text)}</text>')
    d.append(f'<text x="8" y="{y0 + 38}" class="b">plan p1</text><text x="8" y="{y0 + 52}" class="s">role plan</text>')
    d.append(f'<text x="8" y="{y0 + 82}" class="b">build b1</text><text x="8" y="{y0 + 96}" class="s">parent inferred = p1</text>')
    d.append(bar(0, 0, 3, 'p4', 'running'))
    d.append(bar(0, 3, 12, 'p3', 'waiting_for_human (finished at 4)'))
    d.append(bar(1, 12, 21, 'p4', 'running → completed at 21'))
    d.append(line([(T(12), y0 + 46), (T(12), y0 + 64)], dash=True))
    d.append(lab(x0, y0 + 108, 'at 12: human.resumed on b1 closes p1’s wait → p1 completed, b1.human_wait_ms = 9m', 'start'))
    d.append(f'<text x="8" y="{ax + 40}" class="lg">active = elapsed − human_wait_ms. A build without parent_attempt_id is linked to the latest earlier attempt</text>'
             f'<text x="8" y="{ax + 55}" class="lg">of the same factory and work item. Work-item outcome: merged if any linked PR merged, rejected if all closed, else open.</text>')
    d.append('</g>')
    return svg(W, ax + 66 - 55, ''.join(d), 'Attempt state machine as derived by normalize')


if __name__ == '__main__':
    for name, fn in [('architecture', architecture), ('issue-flow', issue_flow), ('db-schema', db_schema),
                     ('code-map', code_map), ('event-path', event_path), ('attempt-states', attempt_states)]:
        with open(os.path.join(HERE, f'{name}.svg'), 'w') as f:
            f.write(fn())
        print(f'wrote {name}.svg')
