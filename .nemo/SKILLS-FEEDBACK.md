# Feedback for the Nemo skills

Collected while building `.nemo/app.xml` (a Nemo v0.7.0 GUI over the horde node
API) — the first real use of these skills. Every item was checked against the
canonical examples in `~/Projects/nemo/examples/` and/or the `nemo` binary.

**Confidence:** ✅ Verified (confirmed against examples/binary) · ❓ Suspected
(couldn't fully confirm).

**Priority:** 🚑 = silent failure — passes `nemo validate` but renders nothing
or never loads. These are the worst for a first-time user and should be fixed
first.

---

## 1. `nemo-xml-reference/SKILL.md`

### 1a. 🚑 ✅ `<tabs>` children are `<tab-item>`, not `<panel>`

`<panel>` is valid anywhere, so a config with `<panel>` tab pages **passes
validation** but the tab bar has zero pages and the whole component renders
invisibly. This was the single biggest blocker.

**Current (lines 146–153):**
```xml
### tabs
<tabs active-tab="0" variant="segmented">
  <panel title="Tab 1"> ... </panel>
  <panel title="Tab 2"> ... </panel>
</tabs>
```

**Replace with:**
```xml
### tabs
<tabs id="my_tabs" active-tab="0">
  <tab-item id="t1" label="Tab 1"> ...content... </tab-item>
  <tab-item id="t2" label="Tab 2"> ...content... </tab-item>
</tabs>
```
- Tab pages **must** be `<tab-item id="..." label="...">`. Any other child
  makes the tabs render nothing (validation will NOT catch it).
- `active-tab` (0-based initial tab) is real — it's in the binary property table.
- ❓ `variant="segmented"` could **not** be confirmed as a `<tabs>` attribute
  (there are `tab_bar.segmented.*` theme keys, but no evidence `variant` is
  wired to tabs). Verify before documenting, or drop it.
- Evidence: `examples/components/app.xml` uses `<tab-item>` ~39×.

Same fix applies to the child-only elements the reference should list explicitly
as "only valid inside their parent": `tab-item` (tabs), `menu-item`
(dropdown-button), `option` (select), `list-item` (list), `accordion-item`
(accordion), `sidenav-bar-item` (sidenav-bar), `slot` (template).

### 1b. 🚑 ✅ Source `interval` is in SECONDS, not milliseconds

**Current (lines 71–72):**
```xml
<source name="ticker" type="timer" interval="1000" />
<source name="api" type="http" url="https://api.example.com" interval="30000" method="GET" />
```

**Replace with:**
```xml
<source name="ticker" type="timer" interval="1" />   <!-- every 1 second -->
<source name="api" type="http" url="https://api.example.com" interval="30" />  <!-- every 30 seconds -->
```
- `interval` is **seconds**. `interval="5000"` = poll every ~83 minutes, so
  data appears to never load. The `1000`/`30000` examples strongly imply ms.
- `refresh="30"` is an accepted alias (`examples/basic`); internal config key is
  `poll_interval`. Pick one to document as canonical and mention the alias.
- Evidence: `examples/data-binding/app.xml` (`interval="30"`) + its README
  ("polls every 30 seconds"); timer sources use `interval="1"`.

### 1c. 🚑 ✅ Scalar bindings use `transform`, not a dot-path; `select:` is unproven

**Current (lines 85–93):**
```xml
<table id="my_table">
  <binding source="data.api" target="rows" />
  <binding source="data.api" target="data" transform="select:name,value" />
</table>
<label id="temp">
  <binding source="mock.temperature" target="text" />
</label>
```

**Replace with:**
```xml
<!-- Scalar field of a source object -> label/text: name the field in `transform` -->
<label id="mode">
  <binding source="data.node" target="text" transform="mode" />
</label>

<!-- Whole array or nested subtree -> table/chart: target="data", no transform -->
<table id="agents">
  <binding source="data.agents" target="data" />
</table>
<table id="nodes">
  <binding source="data.cluster.nodes" target="data" />  <!-- nested path OK for target=data -->
</table>

<!-- Shorthand binding attribute on <text>: -->
<text id="raw" content="waiting…" bind-content="data.api" />
```
- To pull a **scalar field** out of a source object, `transform` names the field
  (or a Rhai transform fn), e.g. `transform="origin"`. `source="data.api.origin"`
  is NOT the pattern for scalars.
- Nested dot-paths **do** work for `target="data"` (subtree → table/chart):
  `data.stats.timeseries.temperature`.
- ❓/❌ `transform="select:name,value"`: **no evidence** in the binary or any
  example. Remove it, or add a working example if it exists.
- `bind-content` on `<text>` is real and undocumented (`examples/data-binding`).
- Evidence: `examples/data-binding` (`transform="origin"`, `bind-content`),
  `examples/data-streaming` (`data.stats.summary` → `target="data"`).

### 1d. ✅ Every handler — including `on-load` — takes `(component_id, event_data)`

**Current (line 105):**
> The `<script>` element accepts an `on-load` attribute naming a Rhai function
> run once at startup…

**Add:**
> Nemo invokes **every** handler (including `on-load`) with two arguments,
> `(component_id, event_data)`. Rhai resolves functions by name **and arity**, so
> a zero-parameter handler fails at runtime with `Function not found: <name>`.
> Signature must be:
> ```rhai
> fn init_handler(component_id, event_data) { ... }
> ```

### 1e. 🚑 ✅ A `<table>` bound to async data MUST declare explicit `columns`

Also an upstream **bug**, not just a docs gap. A `<table>` with only a
`<binding target="data">` and no `columns` attribute renders **nothing** (rows
present, zero columns) when the data arrives asynchronously (e.g. from an HTTP
source).

Root cause (`crates/nemo/src/components/table.rs` + `app.rs`
`get_or_create_table_state`): columns are auto-detected from the first row
**only at delegate construction**. A bound table is constructed before the first
fetch returns, so rows are empty → columns auto-detect to empty. When data
arrives, the state path calls `set_rows()` + `refresh()` but **never recomputes
columns**. So auto-detect only works for tables whose data is present at build
time (inline `data=`, or a synchronous timer source that ticked first).

Docs fix: state that any `<table>`/`<tree>` bound to a data source must declare
`columns='[{"key":"..","label":".."}]'`. Both working examples do
(`data-streaming` `stats_table`, `components` `table_demo`).

Upstream fix suggestion: in `get_or_create_table_state`, when `columns` weren't
explicitly configured, recompute them from the first row on data change (not
just `set_rows`).

Evidence: verified by reading the source; reproduced against a live HTTP source
returning `[{...}]`.

### 1f. ❓ Carry over the 0px-collapse rule for app authors

Add a note (it currently lives only in the component-patterns skill, which app
authors don't read): components with no definite-height ancestor collapse to
0px. Wrap scrollable content in `<stack scroll="true" flex="1">` instead of
hard-coding heights. This bit me on tables and the tabs region.

---

## 2. `nemo-plugin-patterns/SKILL.md`

### 2a. ✅ State the handler calling convention next to the Rhai section

Around line 194 ("Available Rhai Functions"), the function list is accurate but
never states the calling contract. Add:

> All XML-referenced handlers (`on-click`, `on-change`, `on-load`, …) are called
> as `fn name(component_id, event_data)`. Match that arity exactly — Rhai
> resolves by name+arity, so a mismatch yields `Function not found`.

Same root cause as 1d; worth stating in both places since they serve different
tasks.

---

## 3. `nemo-component-patterns/SKILL.md`

### 3a. Scope is contributor-facing, not app-author-facing

This skill documents building components **in the Nemo Rust source** (4-file
pattern, `RenderOnce`, registry schema). When writing an `app.xml` almost none of
it applied, but the frontmatter description ("writing or modifying Nemo
components") reads like it might. Suggest sharpening the description to something
like: *"…for contributing new components to the Nemo source tree (Rust). Not for
authoring app.xml configs — see nemo-xml-reference for that."*

### 3b. The "Height Gotcha" (line 105) is the one broadly useful bit

It's accurate and hit me as an app author too. Consider surfacing a
config-facing version in `nemo-xml-reference` (see 1e).

---

## 4. Cross-cutting

### 4a. ✅ `nemo validate --strict` false-positives on child-only elements

`--strict` reports `tab_item` as `unknown-component`; the official
`examples/components/app.xml` fails `--strict` the same way yet renders fine.
Plain `nemo validate` passes. Any skill/scaffold that recommends `--strict`
should either switch to plain `validate`, or the validator should special-case
the child-only elements listed in 1a.

### 4b. Anchor skill snippets to `examples/`

Every divergence above is a spot where a skill drifted from Nemo's own examples,
which are ground truth and actually run. Generating/spot-checking the snippets
against `~/Projects/nemo/examples/` (esp. `components`, `data-binding`,
`data-streaming`, `complete`) would catch all of these.

---

## Quick triage order

1. 1a (tabs children) — silent, total-blocker.
2. 1b (interval seconds) — silent, "nothing ever loads".
3. 1e (table needs explicit `columns` when bound) — silent, empty table; also an upstream bug.
4. 1c (binding scalar/`select:`) — wrong data wiring.
5. 1d / 2a (handler arity) — clear runtime error, easy to hit on the first handler.
6. 4a (`--strict`) — misleading validation advice.
7. Everything else — polish.

## Upstream bugs (vs. docs fixes)

Most items above are doc fixes, but two are real Nemo defects worth filing:
- **1e** — bound tables never recompute auto-detected columns; empty table.
- **4a** — `nemo validate --strict` rejects valid child-only elements (`tab_item`).
