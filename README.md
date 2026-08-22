# Kong Flow

A Node-RED–style visual manager for **multiple Kong API Gateways**. Each registered Kong is a
workspace; inside it the topology (Services, Routes, Plugins, Consumers, Upstreams, Targets) is a
canvas of nodes wired by their real Admin API relationships — and it is editable, not just a viewer.

Implements the MVP in [SPEC.md](SPEC.md).

![canvas](docs/canvas.png)

## What works today

| Spec item | Status |
|---|---|
| Register N Kongs, test connection, switch workspace | ✅ |
| Admin API auth: none, API key, RBAC token, bearer, basic, **OAuth2 client credentials** | ✅ |
| Filter the canvas by name or uuid, per entity kind | ✅ |
| Auto-rendered graph (services, routes, plugins, consumers, upstreams, targets) | ✅ |
| Dynamic plugin property form from `/schemas/plugins/{name}` | ✅ |
| Create / edit / delete nodes, drag-to-connect, cascade-aware delete | ✅ |
| Validation of what Kong requires, before an apply is attempted | ✅ |
| Copy/paste a whole node — Service + Routes + Plugins + its Upstream and Targets | ✅ |
| Several people on the same Kong at once: **one shared draft**, live pointers, shared dragging, presence, stale-delete protection, conflict detection | ✅ |
| Undo / redo on the canvas (<kbd>Ctrl</kbd>+<kbd>Z</kbd> / <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>Z</kbd>) | ✅ |
| Roll back an applied run against Kong, conflict-checked and itself recorded | ✅ |
| Change requests: everyone plans and proposes, only approvers push to Kong | ✅ |
| Draft state + `terraform plan`-style diff before touching Kong | ✅ |
| Apply in dependency order, live progress over WebSocket, apply history | ✅ |
| Export **and** import decK `kong.yaml` | ✅ |
| Canvas layout persisted per Kong (dagre auto-layout for anything new) | ✅ |
| Automatic rollback on partial failure | ❌ — apply stops at the first error and logs it (per spec §10.5); undoing it afterwards is a button |
| Real authentication (OIDC / SSO) behind the approver role | ❌ — the actor is a self-declared name plus a shared approval token |
| Konnect, K8s ingress | ❌ — post-MVP |

Editing is always two-phase: the canvas is a **draft**, and nothing reaches the Admin API until
*Review changes → Apply*. The plan is recomputed against the live state at apply time, so a stale
diff can never be applied.

Two rules protect the draft:

* **Nothing incomplete is sent.** A Service with no host, a Route with no matcher, a Target with no
  Upstream — the canvas flags these on the node, in the property panel and in the review panel, and
  refuses to apply until they are resolved, instead of letting Kong reject them mid-run.
* **Nothing that would collide is sent.** Kong's uniqueness rules are checked upfront too: a name
  another Service already uses, or a second `key-auth` on a Route that has one. Those are 409s the
  Admin API would answer with halfway through a run; here they are named before it starts.
* **A failed apply never costs you work.** Whatever Kong refused or skipped stays on the canvas,
  still wired up and still pending; only what actually succeeded is replaced by Kong's own copy.

## Quick start — demo stack

One command brings up Kong 3.9.1, a seeded topology and Kong Flow itself:

```bash
cp .env.example .env      # optional: set KONGFLOW_SECRET_KEY
make demo
```

To try the shared canvas with somebody else, set `KONGFLOW_BIND=0.0.0.0` in `.env` before `make demo`
and open the machine's LAN address instead of `localhost`. It stays off by default, and it never
exposes Kong's Admin API — see [Configuration](#configuration).

| | URL |
|---|---|
| **Kong Flow UI** | **http://localhost:8080** |
| Kong proxy | http://localhost:8000 |
| Kong Admin API | http://localhost:8001 |

The workspace **Demo Kong** registers itself, so the canvas is populated on first load.

Every seeded route answers with **canned JSON via the `request-termination` plugin**, so nothing
real has to be running behind the gateway:

```bash
make demo-check
GET    /orders                -> 200 {"data":[{"id":"ord_1001","customer":"mobile-app",…
POST   /orders                -> 201 {"id":"ord_1003","status":"created",…
GET    /users/me              -> 200 {"id":"usr_42","username":"mobile-app",…
GET    /users/admin           -> 401 {"message":"No API key found in request"…
GET    /payments/status       -> 503 {"error":"payments provider under maintenance",…
```

The topology is deliberately built to show every node type and plugin-ordering effect:

* `orders-api` — two routes with dummy `200`/`201` bodies, plus a service-scoped
  `request-size-limiting`.
* `users-api` — `/users/me` returns a dummy user; `/users/admin` also carries `key-auth`, which has
  a higher priority than `request-termination` and therefore answers **401** before the dummy body
  is ever produced.
* `payments-api` — points at the **Upstream** `payments-pool` by name (the dashed edge on the
  canvas) and its route pairs `rate-limiting` (5/min) with a dummy **503**, so the 6th call in a
  minute becomes a **429**.
* Consumers `mobile-app` and `partner-portal`, the latter with a consumer-scoped dummy `429`.
* A global `correlation-id`.

Re-running `make demo` is safe — the seed upserts by name and pins plugin ids.
`make demo-down` removes everything, volumes included.

## Quick start — development

```bash
make demo-kong   # Kong + the demo data on :8000 / :8001, and Postgres on :5432
make api         # backend on :8080
make ui          # Vite dev server with hot reload
```

Open **http://localhost:5173**. Or skip Vite and serve everything from the Go binary on
**http://localhost:8080** with `make serve`.

Register a Kong by clicking **+** in the sidebar (`http://localhost:8001`, auth *None*).

Run the tests with `make test` (`make test-backend` / `make test-frontend` individually). The
backend tests run against a real PostgreSQL — `make test-backend` starts a throwaway one on `:5433`
and gives each test its own schema; `make test-db-down` removes it. Point somewhere else with
`KONGFLOW_TEST_DATABASE_URL`. Without a reachable database those tests skip instead of failing.

## Deployment

```bash
docker compose up -d --build
```

Two containers: the Go binary serves the built SPA and the API on `:8080`, and **PostgreSQL 18**
holds the tool's own state (registered Kongs, canvas layout, apply history) in the `kong-flow-db`
volume. Only `:8080` is published, on loopback — put it behind the same Cloudflare Tunnel / Zero
Trust as the rest of your services rather than exposing it directly.

To use a PostgreSQL you already run, set `KONGFLOW_DATABASE_URL` and drop the `db` service. The
schema is created and migrated on start; the account needs `CREATE` on its schema.

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `KONGFLOW_ADDR` | `:8080` | Listen address |
| `KONGFLOW_DATABASE_URL` | `postgres://kongflow:kongflow@localhost:5432/kongflow?sslmode=disable` | PostgreSQL 18 connection string |
| `KONGFLOW_DATA_DIR` | `./data` | Generated encryption key |
| `KONGFLOW_SECRET_KEY` | *(generated)* | AES-GCM key for stored Admin API credentials |
| `KONGFLOW_CORS_ORIGINS` | `http://localhost:5173` | Comma-separated allowed browser origins |
| `KONGFLOW_BIND` | `127.0.0.1` | Address the published ports bind to. `0.0.0.0` reaches the UI and the Kong proxy from the network; it never opens Kong's Admin API or PostgreSQL |
| `KONGFLOW_APPROVERS` | *(unset)* | Comma-separated names allowed to apply to Kong. Unset ⇒ everyone applies directly |
| `KONGFLOW_APPROVER_TOKEN` | *(unset)* | Shared secret required to approve. Set it together with `KONGFLOW_APPROVERS` |
| `KONGFLOW_STATIC_DIR` | *(unset)* | Serve a built SPA from this directory |
| `KONGFLOW_DB_USER` / `KONGFLOW_DB_PASSWORD` / `KONGFLOW_DB_NAME` | `kongflow` | Credentials for the `db` service in `docker-compose.yml` |
| `KONGFLOW_TEST_DATABASE_URL` | `postgres://kongflow:kongflow@localhost:5433/kongflow_test?sslmode=disable` | Database the backend tests use |
| `KONGFLOW_PORT` / `KONG_PROXY_PORT` / `KONG_ADMIN_PORT` / `KONGFLOW_DB_PORT` | `8080` / `8000` / `8001` / `5432` | Host ports for the demo stack |

Copy `.env.example` to `.env` and fill it in — Compose reads it automatically.

Set `KONGFLOW_SECRET_KEY` from your secret manager in production. Without it, a random key is
generated into `$KONGFLOW_DATA_DIR/secret.key` on first start — losing that file means the stored
Admin API credentials can no longer be decrypted.

## Architecture

```
frontend/ (Vue 3 + Vue Flow + Pinia + Tailwind)   backend/ (Go + gin)
  stores/graph.js     shared draft, edit funnel,     internal/kong     Admin API client + snapshot
                      undo/redo, local diff,         internal/plan     diff engine, ordered apply,
                      draft ids, dagre layout                          rollback of a recorded run
  stores/session.js   identity, approver role,       internal/deck     kong.yaml import/export
                      presence, pointers             internal/store    Postgres: connections, layout,
  stores/requests.js  the approval queue                               history, change requests
  components/         canvas, property panel,        internal/api      gin router, WebSocket hub,
                      diff, history, queue                             approval queue, rollback
```

The browsers hold the shared draft; PostgreSQL holds what the tool owns (registered Kongs, canvas
layout, apply history, queued change requests). Kong itself is never duplicated — it is read live
and remains the only source of truth for what is deployed.

### Finding things on a big canvas

The filter sits collapsed in the top-left corner as a single field — a magnifier and a text box,
nothing else. Clicking it (or pressing <kbd>/</kbd> anywhere on the canvas) unfolds the kind chips,
the match counter and the result list; clicking back on the canvas folds it away again while the
filter keeps running, with a small badge showing how many nodes still match so a dimmed canvas is
never unexplained.

It matches case-insensitively, by substring, on the fields that actually identify each kind — plus
the Kong uuid, which everything has:

| Kind | Searched |
|---|---|
| Service | name, **host** |
| Route | name, **paths**, hosts, methods |
| Plugin | name |
| Consumer | username, custom_id |
| Upstream | name |
| Target | target address |

So `/payments/status` finds the Route serving it, and `payments-pool` finds both the Upstream of
that name and the Service pointing at it. List fields match on any entry, so one path out of five
is enough. The kind chips narrow the search further.

Matches stay lit while everything else fades, so the surrounding topology is still readable;
**Hide the rest** removes the non-matching nodes (and any edge that would dangle) when the graph is
too dense. Each result is listed with what matched and its short uuid — clicking one selects and
recentres it. Filtering is purely a view: it never changes what an apply sends to Kong.

![filter](docs/filter.png)

Only three things are stored locally: **registered connections** (secret encrypted at rest),
**node positions** (a canvas concept Kong has no idea about) and **apply history**. Services,
routes and plugins are always read live from the Admin API, so the tool cannot drift from reality —
hit **Refresh** if something changed outside it.

### Editing model

* Entities created on the canvas get a `draft:` id. `internal/plan` matches desired against live
  state by id, emits `create` / `update` / `delete` ops, and orders them so dependencies exist
  before their referrers (and are deleted after them).
* Updates are **field-level**: only what actually changed is sent (`PATCH`), so version-specific
  fields the UI does not know about are never clobbered. Plugin `config` is sent whole, so editing
  one key cannot reset the rest to defaults.
* During apply, foreign keys still pointing at `draft:` ids are rewritten to the real ids Kong
  returned for entities created earlier in the same run — on the canvas too, so a draft that failed
  now points at the sibling that succeeded and can simply be applied again.
* If a run fails partway, the live state becomes the new baseline while every unapplied entity is
  kept, and the plan is rebuilt so the review panel shows what is still outstanding.
* Targets are immutable in Kong, so an "update" is executed as delete + create.

The persist path is the part most worth trusting, so it is covered from both ends: Go tests assert
the exact JSON body that reaches the Admin API (plugin `config` whole, draft references resolved,
`false`/`0`/`""` preserved, PATCH kept minimal), and Vitest covers the store and the property panel
that produce it.

### Node relations the canvas understands

| Drag from → to | Becomes |
|---|---|
| Service → Route | `route.service` |
| Service / Route / Consumer → Plugin | `plugin.service` / `.route` / `.consumer` (a plugin has exactly one owner) |
| Upstream → Target | `target.upstream` |
| Upstream → Service | `service.host = upstream.name` (dashed edge — Kong links these by name) |

Right-click is how everything is created: on empty canvas it offers the six node types, paste and a
re-layout; on a node it offers edit, copy, duplicate and delete; on an edge it offers disconnect
(double-clicking an edge does the same). A **Route** additionally offers **Copy URL** — the address
it actually answers on, one entry per path.

A Plugin's name is the plugin *type*, so it is chosen from the list the gateway reports rather than
typed, and duplicating one keeps that type instead of inventing a `-copy` variant Kong has never
heard of. The duplicate lands on the same Service or Route as the original, where Kong only allows
one plugin of each type — so the canvas says exactly that, and the fix is to drag it onto a
different one.

### Moving work between Kongs

**Copy** on a node (or <kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>C</kbd>) puts it on the **system
clipboard** as JSON, together with everything that belongs to it. A Service travels with:

* every **Route** on it, and every **Plugin** on the Service or on those Routes;
* the **Upstream** its `host` names, and that Upstream's **Targets**.

That last part is what makes a copy usable on the other side: a Service points at its balancer by
name, so without the Upstream it lands in the target Kong addressing a host nothing answers on. It
is deliberately wider than the delete cascade — deleting a Service must leave a shared Upstream
alone, copying one must not.

<kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>V</kbd> on any canvas pastes it back as drafts, wired the same way
— including in another workspace, another browser window, or a colleague's message, since it is
ordinary text.

Ids from the source gateway are stripped, and references pointing outside the bundle are dropped:
copying a Route alone leaves it serviceless in the target, because the Service it belonged to lives
in a different Kong. **Names that are already taken are moved aside** (`billing` → `billing-copy` →
`billing-copy-2`), so pasting back into the Kong you copied from is not a 409 waiting to happen —
and a renamed Upstream drags the pasted Service's `host` along with it. Pasting twice is safe, and
the usual review-then-apply still stands.

<table>
<tr>
<td width="33%"><img src="docs/context-menu-canvas.png" alt="Right-click on the canvas: add Service, Route, Plugin, Consumer, Upstream, Target, or auto-layout"></td>
<td width="33%"><img src="docs/context-menu-node.png" alt="Right-click on a Route node: copy URL, edit properties, duplicate, delete"></td>
<td width="33%"><img src="docs/context-menu-edge.png" alt="Right-click on the edge between a Service and a Route: disconnect"></td>
</tr>
<tr>
<td><em>On empty canvas — a new node is placed where you clicked.</em></td>
<td><em>On a node — a Route also offers its public URL; delete confirms first, listing what cascades.</em></td>
<td><em>On an edge — disconnecting clears the foreign key behind it, here <code>route.service</code>.</em></td>
</tr>
</table>

## Working alongside other people

Several people can have the same Kong open. Kong itself is the shared state — this tool keeps no
copy of it — so what has to be handled is everything around that.

**Nobody deletes what they never saw.** The canvas sends the backend both the state it wants *and*
the baseline it was loaded from. A plan can then tell "the user removed this" apart from "somebody
added this ten minutes after this tab opened": the second is reported as left untouched, never as a
delete. Without the baseline — the old behaviour — an old tab pressing Apply wiped out everything
created since it loaded.

**Nobody silently overwrites an edit.** If an entity changed in Kong after the canvas read it, the
plan comes back with a **conflict**: what it is, and field by field what the other person did. The
apply is refused with a 409 until you either refresh (take their version) or say explicitly that
yours wins.

**One apply at a time per Kong**, enforced with a PostgreSQL advisory lock, so two runs cannot
interleave their operations — across replicas of this server, too.

![two people on the same Kong](docs/collaboration.png)

*Two browsers on the same gateway: the toolbar counts the other editor, their pointer moves across
the canvas with their name on it, and the Route they have open is ringed and initialled.*

**You can see who else is here, and what they are doing.** The toolbar counts the other editors on
this gateway, a node somebody else has open is ringed and initialled, and their **pointer moves on
your canvas** with their name on it. **Dragging is shared**: a node travels on everyone's screen
while it is being moved, not once it is dropped. When somebody's change lands, every other canvas
gets a "this is out of date — refresh" strip instead of quietly going stale.

**The draft itself is shared.** Adding a Route, editing a plugin's config, deleting a Service, pasting
a bundle, dragging a node — each lands on everyone else's canvas as it happens. Nothing there has
touched Kong: it is still one draft that several people are building together, and it still takes a
Review → Apply to become real.

Every edit goes through one funnel in the store, which works out the entities it changed and sends
that list on. Removals travel as an explicit `null`. The server relays them without looking inside —
it decides who sees an edit, not what an edit means.

A tab that opens a Kong somebody is already working on **asks for the current draft** rather than
starting from what Kong reports and overwriting their work on the first edit. The longest-serving tab
answers, so a room of five hands over one copy, not five. **Refresh** and **Discard** are the way
back: both are deliberate "everyone returns to what Kong says", and both reach the other canvases.

Pointers and drags travel in **flow coordinates**, so they land on the same node for everybody
regardless of how each person has panned and zoomed. They go out at 25 frames a second, are relayed
straight to the other canvases rather than through the presence roster — which would otherwise
rebuild and re-send the whole peer list on every mouse move — and are **not sent at all while you
are alone on a Kong**, which is the common case. None of it is persisted: pointers die with the
socket.

Node positions are the one exception, because layout is shared per Kong (`canvas_layout`, not
per user): the person who dragged writes the final position, everybody else just follows. A frame
for a node you are dragging yourself is ignored, so two people cannot fight over one node mid-drag.

### Undo, redo, and rolling back

<kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>Z</kbd> takes back the last thing **you** did;
<kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>Z</kbd> (or <kbd>Ctrl</kbd>+<kbd>Y</kbd>) puts it back. The
toolbar has both, and each names what it would undo. Fifty steps are kept, and a cascading delete,
a paste and a drag are each a single step — undoing a deleted Service brings its Routes and their
plugins back wired as they were.

Undo is **local by design**: it takes back your own work, never the edit somebody else just made.
An undo is published like any other edit, so everyone's canvas follows it.

> Where this is honest about its limits: two people editing the *same entity* at the same time is
> last-write-wins, and an undo restores that entity wholesale — so undoing your edit to a Service
> that somebody changed in between takes their change with it. Coming from a Kong of a few people
> working on different services, that is a fair trade for a mechanism you can reason about.

Rolling back is the other half, and it is about Kong rather than the canvas. Every run is in the
**History** panel, and any of them can be reverted: what it created is deleted, what it updated goes
back to the values it had, what it deleted is recreated **with the id it used to hold**. Only the
operations Kong actually accepted are inverted, so a run that failed halfway undoes just the part
that landed.

The plan is **rebuilt against Kong when you press the button**, not stored from when the run
happened — so a run from last week is judged on today's gateway. Anything already undone by hand is
left out; anything changed since comes back as a conflict you have to accept explicitly. The
rollback is applied under the same lock as any other apply and is **recorded in the history itself**,
so it can be rolled back in turn.

### Change requests: who may actually touch Kong

By default there are no approvers configured and everyone applies directly — a single-operator
install behaves exactly as it always did.

Set **`KONGFLOW_APPROVERS`** (and/or **`KONGFLOW_APPROVER_TOKEN`**) and the queue turns on:

| | Editor (default) | Approver |
|---|---|---|
| Read the topology, edit the canvas, build a plan | ✅ | ✅ |
| Press *Apply* | ✅ — files a **change request**; nothing reaches Kong | ✅ — runs against Kong |
| Approve / reject somebody's request | ❌ | ✅ |
| Preview what rolling a run back would do | ✅ | ✅ |
| Actually roll a run back | ❌ | ✅ |
| Withdraw a request | own only | any |

A request stores the proposed canvas **and** the baseline it was built on. When an approver opens
it, the plan is **rebuilt against Kong as it is at that moment** — not as it was when the request
was written — so a change that sat in the queue overnight is judged on today's gateway, conflicts
and all. Approving runs it under the same lock and records it in the apply history, naming both the
approver and the author.

> **What this is and is not.** There is no login yet: the actor is a name the browser declares, and
> `KONGFLOW_APPROVER_TOKEN` is the only part that actually authenticates. Configure both — the token
> is required to approve, and the name list narrows who may use it. Treat the name alone as a
> convention inside a trusted team; put real authentication (SSO, or an authenticating proxy setting
> `X-KongFlow-Actor`) in front of this before it guards a production gateway.

## Authenticating against the Admin API

Each registered Kong picks its own scheme: none, an API key header, an Enterprise RBAC token, a
bearer or basic value, or **OAuth2 client credentials**.

Each connection also carries an optional **proxy base URL** — where that Kong *serves traffic*, as
opposed to where it is administered. Nothing calls it; it is what turns a Route's `paths` into a
copyable address (`https://api.example.com` + `/orders`). Without it, Copy URL falls back to the
Route's own `hosts`, and says so when the Route has none.

### OAuth2 client credentials

You supply the **token URL**, **client id** and **client secret**; `grant_type=client_credentials`
is always implied. The token request is the RFC 6749 §4.4 shape — the three parameters travel in
the form-urlencoded body, never as headers, so they cannot end up in a proxy access log:

```http
POST /oauth2/token HTTP/1.1
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials&client_id=kong-flow&client_secret=%E2%80%A6
```

The response is read as JSON:

```json
{ "access_token": "…", "expires_in": 3600, "scope": "kong:admin", "token_type": "Bearer" }
```

What happens from there:

* The token is cached **per registered Kong**, on the server, so one token serves every browser
  request instead of being minted per call.
* Before any Admin API action the expiry is checked and the token re-minted when it has run out.
  A 30s skew means a token is never sent when it is about to die mid-flight.
* `expires_in` is accepted as a number or a string; when it is missing the token is treated as
  short-lived and renewed often.
* If Kong rejects a token that still looked fresh (clock skew, early revocation), the client mints
  a replacement and retries **once**. Refresh is scoped to the rejected token, so the six parallel
  calls of a snapshot share a single replacement instead of stampeding the authorization server.
* Rotating the client id, secret or token URL retires the cached token immediately.
* The client secret is encrypted at rest with the same AES-GCM key as every other credential, is
  never returned by the API (`has_oauth_secret` says only whether one is stored), and does not have
  to be retyped to edit the connection.

![oauth connection](docs/oauth.png)

**Test connection** separates the two failure modes: a rejected credential is reported as an
`oauth` stage failure carrying the authorization server's own message, while a valid token that the
gateway refuses is reported as an `admin_api` failure. On success it shows the token type, lifetime
and scope.

## Known limitations

* Consumer credentials (key-auth keys, JWT secrets, …) are not modelled yet — only the Consumer
  and the plugins attached to it.
* OAuth2 covers the client-credentials grant only; there is no authorization-code/PKCE flow, since
  the tool authenticates as itself rather than on behalf of a signed-in user.
* Enterprise workspaces are supported as a per-connection prefix; register a Kong once per
  workspace rather than switching inside one entry.
* The decK export mirrors what Kong returns, including server-filled defaults.
* Presence, pointers, drags and apply events are broadcast in-process. Running more than one replica
  needs a shared relay (PostgreSQL `LISTEN`/`NOTIFY`) before the events reach browsers on the other
  replica — the apply lock and the change-request queue already work across replicas, since both
  live in the database.
* Canvas layout is shared, not per user: two people cannot arrange the same Kong differently.
* The shared draft lives in the browsers, not in the database: if every tab on a Kong closes, the
  unapplied draft goes with them. Applying it, or exporting decK, is what makes it durable.
* Concurrent edits to the *same entity* resolve last-write-wins, and undo restores an entity
  wholesale rather than replaying just your own change out of it.
* The approver role is only as strong as `KONGFLOW_APPROVER_TOKEN`; see *Working alongside other
  people* for what that means.
