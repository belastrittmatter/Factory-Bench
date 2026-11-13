# Dashboard user stories

Who uses it: the **operator** running a factory (is it working right now?) and the **factory engineer** tuning one (is config B better than A?).

| # | As a… | I want to… | so that… | View |
|---|---|---|---|---|
| 1 | operator | see at a glance how many attempts run, which went idle (no event within the idle timeout) and what they cost so far | I intervene before a run burns money | Overview, Live |
| 2 | operator | see the spend and token burn per hour, split by model | I spot a runaway early | Overview |
| 3 | operator | see each running attempt's lane, phase, last tool and cost curve | I know what it is doing without opening logs | Live |
| 4 | engineer | filter everything by time range, factory, config and repo | numbers answer one question at a time | all |
| 5 | engineer | sort and filter issues by outcome, cost, time to PR, review rounds and CI | I find the expensive or failing ones | Items |
| 6 | engineer | open one issue and see its attempts on a time axis, PRs, reviews, CI and every event | I understand what happened, in order | Item |
| 7 | engineer | open one attempt and see cost over time, phase split, tool mix, failed tool calls, subagents and API errors | I see where the time and money went | Attempt |
| 8 | engineer | see a card's lane moves (Build → QA → Review → Done) and its bounces back | I find cards that loop | Item (flow) |
| 9 | engineer | pick the factories or config versions to compare and see them side by side, best value per metric marked, with the same numbers as `report factories` | I decide which config to keep | Compare |
| 10 | engineer | see cost, attempts and merges per day per config | I see trends after a config change | Compare |
| 11 | operator | read the transcript, worker log and run manifest of one attempt, only on this machine | I debug a run without leaking prompts | Attempt (local only) |
| 12 | operator | register a factory, issue it a new token, revoke one and fix its commit email or login, only on this machine | I onboard a factory or cut one off without the CLI | Factories (writes local only) |

Rules: unknown is `?`, never 0; nothing to measure is `n/a`; a lower bound is `+?`. Text from GitHub and transcripts is always plain text.
