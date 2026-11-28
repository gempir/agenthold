# agenthold Next-Level Roadmap (T3 Code Experience + Collaboration + Hosting-First + Org Tools)

Living tracker for transforming agenthold into a T3 Code-caliber, browser-first, collaborative, self-hosted agent platform with organization-provided MCP & tools.

Update checkboxes as each milestone merges cleanly to `main` with `make test` and `make lint` green.

## Core Principles
1. **Project & Thread First**: Per-thread harness/model/effort switching (with automatic Context Handoff). Hide VM/infrastructure ceremony from everyday chat.
2. **Optional Collaboration**: Thread-level sharing (Viewer / Collaborator / Guest links), live multiplayer presence, shared diff review comments, per-caller credential isolation.
3. **Hosting-First, Browser-Native**: Zero desktop client; full diff/file/terminal/browser-preview loop in one browser tab, PWA + Web Push, 24/7 always-on PR watching and workflows.
4. **Org-Provided MCP & Tools ("Login Once")**: Admins configure org tools/skills/provider pools once; users click Connect once (or zero setup for org-shared tools) and every thread gets them automatically with credentials never entering sandboxes.

## Reserved Migrations
- `00018_thread_ux.sql` — Thread pinning, snoozing, permission mode, side chats, queued follow-up messages
- `00019_prune_tasks.sql` — Drop unused siloed `project_tasks` table
- `00020_org_tools.sql` — Organization tools/MCP catalog, org skills, org shared provider logins, per-user/project overrides
- `00021_thread_sharing.sql` — Thread-level shares, guest links & approval queue, collaborative review comments, Web Push subscriptions
- `00022_pull_requests.sql` — Linked PRs per thread, PR watching state
- `00023_usage_and_workflows.sql` — Per-turn token/cost usage records, durable multi-stage workflows & approval asks

---

## Phase 1 — Everyday T3 Code Loop (UI & Thread Ergonomics)
- [x] **1A. Per-Thread Harness/Model, Queue & Steer, Rewind, Side Chats, Permission Modes (`00018_thread_ux.sql`)**
  - Allow selecting and switching harness + model + effort per thread (without mutating the whole agent), reusing existing Context Handoff.
  - Persistent message queue per session + steer mid-turn (`POST /sessions/{id}/queue`, `PATCH/DELETE`, `POST .../send-now`, steer endpoint); auto-drain on turn end.
  - Rewind / "Edit from here" (`POST /sessions/{id}/rewind`, optional file revert in worktree).
  - Side chats (`kind=side_chat`, `/side` / `/btw`) seeded with parent context, openable beside the main thread, with "Add to chat".
  - Per-thread permission mode (`full-access` default, `auto-accept-edits`, `supervised`, `plan`) wired into harness/ACP requests.
  - Thread `pinnedAt` and `snoozedUntil` API endpoints (`POST /sessions/{id}/pin`, `/unpin`, `/snooze`, `/unsnooze`).
- [x] **1B. Sandbox Diff & File Viewer/Editor APIs + Project Scripts (`agenthold.json` / `t3.json`)**
  - Extend `sandboxd` + controller API for structured git diffs (`turn`, `uncommitted`, `branch`, `commits` + commit detail), file read/write (UTF-8 text & raw preview, line range), and workspace file search (for `@` file picker).
  - Support reading `agenthold.json` (with fallback to `t3.json`) in project root for project scripts (`Setup` on worktree create, `Dev` with `previewUrl` + `stopOnSettle`, `Test`, `Clean` on settle) and executing them via services/exec.
  - Add optional `isolation: "worktree"` to `start_subthread` platform tool.
- [x] **1C. Right Panel Tabs: Diff Pane, File Viewer/Editor, Side-by-Side Sub-threads & Thread Details Card**
  - Upgrade right panel to multi-tab `RightPanelTabs`: Diff pane (Turn / Uncommitted / Branch / Commits, unified/split toggle, `Cmd+F` find-in-diff, inline line-range review comments → composer chips), File Viewer/Editor (syntax highlighting, rendered/raw Markdown/HTML/CSV/PDF, save edits), side-by-side Sub-thread & Side Chat tabs, and a T3-style Thread Details popover/card (environment, project scripts, branch, dev servers, linked PRs).
  - Clickable `[file:line]` links & syntax-highlighted code blocks with copy button in `Markdown.tsx`.
- [x] **1D. Upgraded Composer (`@` / `$` / `/` / `#`, General Attachments, Quote-to-Cite, Stash, Multi-Model)**
  - Queue & steer UI while running (with queued message list: Send now / Edit / Remove).
  - Inline context chips for files, quoted assistant messages (select text → Cite in composer), diff review comments, threads (`@`), models (`@`), skills (`$`), slash commands (`/`).
  - General file attachments (upload to workspace) + 32 KiB+ large paste → file chip.
  - `ArrowUp` sent-prompt recall, `Cmd+S` prompt stash, `Shift`-click multi-model parallel launch, `Cmd/Ctrl+Enter` background start.
- [x] **1E. Sidebar Redesign, Command Palette (`Cmd+K`) & Clutter Pruning (`00019_prune_tasks.sql`)**
  - Sidebar: **Pinned**, **Active**, **Working** (collapsible running threads), **Snoozed**, **Settled**, optional **Group by project** toggle, Undo toast (`Cmd+Z`), and T3-style bottom-left utility dock (Settings, PRs, Automations, Usage).
  - Global Command Palette (`Cmd/Ctrl+K`) searching threads (titles, branches, message text via backend search endpoint), projects, scripts, settings, and actions.
  - Prune clutter: remove built-in `ProjectTask` mini-tracker (`00019_prune_tasks.sql`, API, tools, UI); remove duplicated workspace tabs (`Files`, `Git`, `Terminal`, `Preview`) from `AgentSettingsPage`; promote Projects out of Settings to first-class navigation; replace manual `GitTab` worktree ceremony with clean in-thread project/branch/worktree controls.

---

## Phase 2 — Organization Tools & "Login Once" Integrations
- [x] **2A. Org Tool Catalog, Org Skills & Org Shared Provider Pools (`00020_org_tools.sql`)**
  - Admin-managed Org Tools (remote HTTP MCP, stdio MCP, Executor catalog apps) with `auth_mode`: `org_shared` (zero-setup shared credential via controller/proxy), `user_oauth` (admin configures once, user connects once), or `none`.
  - Admin-managed Org Skills auto-delivered to every agent's harness skills directory alongside user skills.
  - Admin-managed Org Provider Logins (shared Anthropic/OpenAI/Gemini/OpenRouter API keys or gateways) so new users can chat immediately after SSO login without personal API keys, while still allowing personal logins as override.
  - Auto-enable Executor + Org Tools across threads by default (no per-agent opt-in ceremony); respect admin policy for custom user MCPs.
- [x] **2B. Unified Integrations UI & In-Thread `request_secret` / `connect_account` Cards**
  - Consolidate Settings (`Accounts`, `Tools`, `Git`, `Secrets`, `Provider logins`) into clean, minimal **Integrations** + **Credentials/Secrets** + **Providers** views; hide raw "Executor runtime Start/Restart" internals (auto-start transparently).
  - Add `request_secret` and `request_account_connect` platform MCP tools rendering secure interactive cards inline in chat (never writing secrets to conversation history).

---

## Phase 3 — Multiplayer Collaboration & Git/PR Loop
- [ ] **3A. Thread-Level Sharing, Guest Links & Per-Caller Tool Identity (`00021_thread_sharing.sql`)**
  - Share individual threads with org users (`viewer` / `collaborator`) without sharing the entire VM.
  - Single-thread **Guest links** (live read-only stream + owner-approved prompt queue, 24h expiry, instant revoke).
  - Route controller-side Executor & Org MCP calls using the **prompting user's** identity (or org service credential), enabling safe multiplayer collaboration without leaking owner tokens.
  - Multiplayer thread UX: live typing/viewing presence, shared diff review comments persisted per thread, `@teammate` handoff notifications.
- [ ] **3B. PWA + Web Push Notifications**
  - Web app manifest, service worker, VAPID key generation/storage, subscription management in Settings, and push alerts on turn completion, errors, approvals/questions, guest prompt requests, and watched PR events.
- [ ] **3C. Full Git & Pull Request Hub + 24/7 PR Watching (`00022_pull_requests.sql`)**
  - In-thread Git actions: Commit (with optional AI commit message), Push, Create PR (with AI title/body), auto-link PRs matching thread branch.
  - Dedicated **Pull Requests** view (via GitHub/GitLab credentials in the controller): list PRs, inspect diffs/checks/comments, review & merge, "Work on PR" to launch a worktree thread.
  - `link_pull_request` & `watch_pull_request` platform tools + background poller that wakes the thread on failed CI, new reviews/comments, merge conflicts, or merge/close (auto-settling merged PR threads).

---

## Phase 4 — Hosting-First Superpowers
- [x] **4A. In-Panel Live Browser & Preview (Exposed Ports + Streamed Chromium)**
  - Right-panel **Preview / Browser** tab: instant iframe view for `expose_port` URLs (auto-detected from running dev servers & project scripts) + interactive Chromium session inside the VM (viewport presets, screenshot, click/type/navigate, element picker & visual annotations → composer chips, `preview_*` platform tools for agents).
- [x] **4B. Inline Visual Replies (`html_render`) & MCP Apps**
  - `html_render` platform MCP tool storing sanitized/sandboxed self-contained HTML visuals (charts, diagrams, mockups) rendered inline in `ChatView` with theme adaptation and fullscreen view.
  - Support interactive MCP App UIs rendered below tool calls with safety confirmation for non-read-only tool actions.
- [ ] **4C. Usage & Quota Analytics Dashboard + Durable Multi-Stage Workflows (`00023_usage_and_workflows.sql`)**
  - Persist per-turn token & estimated cost records; build **Usage** view (Cost / Tokens / Models / Users / 24h-7d-30d-90d filters, org-wide admin breakdown, custom model pricing).
  - Always-on durable **Workflows** engine: multi-step pipelines (agent steps, shell/code steps, human approval `ask` cards, schedules, webhooks, concurrency limits) with a live **Workflows** dashboard ("Needs you" / "Running" / "All").
