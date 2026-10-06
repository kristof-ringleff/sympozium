# TUI Dashboard

Running `sympozium` with no arguments (or `sympozium tui`) launches a **k9s-style interactive terminal UI** for cluster-wide agent management.

## Views

| Key | View | Description |
|-----|------|-------------|
| `1` | Ensembles | Ensemble list — press Enter to activate a pack and create agents |
| `2` | Agents | Agent list with status, channels, memory config |
| `3` | Runs | AgentRun list with phase, duration, result preview |
| `4` | Policies | SympoziumPolicy list with feature gates |
| `5` | Skills | SkillPack list with file counts |
| `6` | Channels | Channel pod status (Telegram, Slack, Discord, WhatsApp) |
| `7` | Schedules | SympoziumSchedule list with cron, type, phase, run count |
| `8` | Gateway | SympoziumConfig gateway (base domain, TLS, listeners) and per-agent routes |
| `9` | Pods | All sympozium pods with status and restarts |

`Tab` / `Shift+Tab` cycle between views.

The TUI covers the `job` backend's day-to-day operations. Celln conversations,
AgentHarness chat and model backends are managed in the web dashboard
(`sympozium serve`).

## Keybindings

| Key | Action |
|-----|--------|
| `l` | Logs (pods) / events (resources) |
| `d` | Describe the selected resource |
| `x` | Delete the selected resource (with confirmation) |
| `e` | Edit memory / heartbeat config of the selected agent |
| `R` | Run a task on the selected agent |
| `O` | Launch the onboard wizard |
| `r` | Refresh data |
| `Enter` | Detail / drill in / onboard an Ensemble |
| `Esc` | Go back / return to Agents |
| `?` | Toggle help |

## Slash Commands

| Command | Description |
|---------|-------------|
| `/agents`, `/runs`, `/policies`, `/skills`, `/schedules`, `/ensembles` | Switch views |
| `/run <agent> <task>` | Create and submit an AgentRun |
| `/abort <run>` | Abort a running AgentRun |
| `/result <run>` | Show the LLM response |
| `/status [run]` | Cluster or run status |
| `/channels [agent]` | View channels |
| `/channel <agent> <type> <secret>` | Add a channel to an agent |
| `/rmchannel <agent> <type>` | Remove a channel |
| `/pods [agent]` | Agent pods |
| `/provider <agent> <provider> <model>` | Set provider and model |
| `/baseurl <agent> <url>` | Set a custom base URL |
| `/features <policy>` | Feature gates on a policy |
| `/schedule <agent> <cron> <task>` | Create a SympoziumSchedule |
| `/memory <agent>` | View persistent memory for an agent |
| `/ensemble delete <name>` | Delete an Ensemble |
| `/delete <type> <name>` | Delete a resource with confirmation |
| `/ns <namespace>` | Switch namespace |
| `/onboard` | Interactive setup wizard |
| `/help` | Show help |
| `/quit` | Exit the TUI |
