# Horde — Nemo control panel

A [Nemo](https://github.com/geoffjay/nemo) application that puts a friendly
GUI over a running Horde node's HTTP API — an alternative to the TUI for users
who would rather point and click than use the CLI.

## What it does

The app is a tabbed dashboard over the node API (`/api/v1`, default
`http://localhost:13420`):

- **Overview** — node mode, ID, leader connectivity, and version.
- **Agents** — list running agents and start a new one by name.
- **Projects** — list projects, create one, and pause/resume/finish by ID.
- **Talk to an agent** — send a plain-language task to an agent and see the reply.
- **Cluster** — nodes registered with this master.

Read-only panels poll the API every few seconds, so the views refresh on their
own after any change. Buttons call the API via the Rhai handlers in
`scripts/handlers.rhai`.

## Run

Start a node first (`horde serve`), then:

```bash
nemo dev --app-config app.xml
```

`nemo dev` hot-reloads the app when you edit `app.xml` or files under `scripts/`.

## Validate

```bash
nemo validate app.xml
```

Do **not** use `--strict` here: its component-registry check doesn't know about
child-only elements like `<tab-item>` (a child of `<tabs>`) and reports them as
"unknown-component". The official Nemo gallery example fails `--strict` the same
way while rendering fine, so plain `validate` is the correct check for this app.

## Pointing at a different node

The API base URL appears in **two** places that must agree:

- the `api_base` variable in `app.xml` (used by the polling data sources), and
- the `api_base()` function in `scripts/handlers.rhai` (used by the buttons).

Edit both if your node listens somewhere other than
`http://localhost:13420/api/v1`.
