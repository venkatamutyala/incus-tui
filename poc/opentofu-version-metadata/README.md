# PoC: publishing dependency versions into the state file

A single-file OpenTofu module that answers *"what versions is this state actually
running?"* — for the CLI, every provider, and every module — and republishes the
answer as outputs, so the metadata ends up **in the state file**, where none of it
lives today.

- The module: [`modules/version-metadata/main.tf`](modules/version-metadata/main.tf) — one file, drop-in.
- The test situation: [`test/`](test/) — one state, ten modules, every source and pinning style mixed together.

## Why it needs three sources

None of this is available from a single place:

| Source | Gives you | Doesn't give you |
| --- | --- | --- |
| `.terraform.lock.hcl` | provider versions, constraints, hashes | anything about modules |
| `.terraform/modules/modules.json` | every installed module, its resolved registry version / git ref, nested children | whether the caller *pinned* it — only what got resolved |
| the `*.tf` files | the declared `source` / `version` constraint per module block | what those resolved to |

So the module reads all three, joins them on the manifest's key space, and adds one
more thing the files don't have: for anything fetched from a VCS it reads the
checkout's `.git/HEAD` and resolves `?ref=main` into the **exact commit** being
applied.

The CLI version comes from shelling out to `tofu version` via the `external`
provider — OpenTofu has no built-in `terraform.version` value. Set
`collect_cli_version = false` to drop that dependency.

## Usage

```hcl
module "version_metadata" {
  source = "./modules/version-metadata"

  extra_metadata = {   # optional free-form passthrough
    component = "my-component"
  }
}

output "dependency_metadata" { value = module.version_metadata.metadata }
output "dependency_fingerprint" { value = module.version_metadata.fingerprint }
```

All inputs are optional; the defaults work for a normal `tofu apply`.

| Input | Default | Purpose |
| --- | --- | --- |
| `root_path` | `path.root` | Point at a fixture directory in tests |
| `lock_file` | `<root>/.terraform.lock.hcl` | Override the lock file location |
| `modules_manifest` | `<root>/.terraform/modules/modules.json` | Override the manifest location |
| `scan_config_sources` | `true` | Parse `*.tf` to recover declared constraints |
| `resolve_git_commits` | `true` | Read `.git/HEAD` of VCS-sourced modules |
| `collect_cli_version` | `true` | Shell out for `tofu version` |
| `warn_on_floating` | `true` | Emit a `check` warning for unpinned deps |
| `extra_metadata` | `{}` | Merged into the published metadata |

| Output | What it is |
| --- | --- |
| `metadata` | Everything: `cli`, `summary`, `providers`, `modules`, `extra` |
| `metadata_json` | The same object as a JSON string — easiest thing to diff or ship |
| `fingerprint` | `sha256(metadata_json)`; changes if any version, ref or commit moved |
| `cli` | Binary product/version, root `required_version`, workspace |
| `providers` | Per provider: version, constraint, constraint kind, hash count |
| `modules` | Per module: kind, source, declared vs resolved version, ref, resolved commit, pin strength |
| `summary` | Counts by kind / pin strength / constraint kind |
| `not_reproducible` | The actionable list: floating modules, tag-pinned modules, unpinned providers |
| `report` | Human-readable table (`tofu output -raw report`) |

### Pin strength

Every module is classified so the interesting question — *would re-running `init`
tomorrow get me the same code?* — is answerable directly:

- **immutable** — a commit SHA, or an exact registry version.
- **mutable** — a git tag: a fixed point, but one someone can move.
- **floating** — a branch, no ref at all, or a `~>` / range registry constraint.
- **local** — a path in this repo; versioned with the repo.

## The test situation

`test/` is one state file containing ten modules, deliberately mixed, plus the
module under test:

| # | Module | Source | Pinning |
| --- | --- | --- | --- |
| 1 | `label_registry_exact` | registry | `version = "0.25.0"` |
| 2 | `label_registry_pessimistic` | registry | `version = "~> 0.25.0"` |
| 3 | `label_registry_unpinned` | registry | no `version` at all |
| 4 | `dir_template_range` | registry | `version = ">= 1.0.0, < 2.0.0"` |
| 5 | `label_git_tag` | GitHub `git::` | `?ref=0.25.0` (tag) |
| 6 | `label_git_commit` | GitHub `git::` | `?ref=488ab91…` (commit SHA) |
| 7 | `label_git_branch` | GitHub `git::` | `?ref=main` (branch) |
| 8 | `label_git_default` | GitHub shorthand | no ref → default branch |
| 9 | `network` | local path | `./modules/network` |
| 10 | `app` | local path | `./modules/app`, which nests `app.tags` |

Plus five providers pinned five different ways: exact (`null`), pessimistic
(`random`), range (`local`, and `external` via the module itself) and completely
unpinned (`time`).

Modules 1–3 and 5–8 point at the same small upstream repo on purpose — the thing
under test is how each *source and pinning style* is recorded, and one small
dependency keeps `tofu init` fast.

```sh
cd test
tofu init
tofu apply
tofu output -raw dependency_report
```

Verified against OpenTofu v1.12.6. Real output from that run:

```text
CLI            : OpenTofu v1.12.6
required_version: >= 1.6.0
workspace      : default

PROVIDERS (5)
  registry.opentofu.org/hashicorp/external             2.4.1      range        >= 2.0.0
  registry.opentofu.org/hashicorp/local                2.9.0      range        >= 2.4.0, < 3.0.0
  registry.opentofu.org/hashicorp/null                 3.2.4      exact        3.2.4
  registry.opentofu.org/hashicorp/random               3.9.0      pessimistic  ~> 3.6
  registry.opentofu.org/hashicorp/time                 0.14.1     unconstrained (unconstrained)

MODULES (12)
  app                          local     local_path              local      ./modules/app
  app.tags                     local     local_path              local      ./modules/tags
  dir_template_range           registry  registry_range          floating   1.0.2
  label_git_branch             git       git_branch              floating   38364d79c1082de13373666252e1276975919543
  label_git_commit             git       git_commit              immutable  488ab91e34a24a86957e397d9f7262ec5925586a
  label_git_default            git       git_default_branch      floating   38364d79c1082de13373666252e1276975919543
  label_git_tag                git       git_tag                 mutable    488ab91e34a24a86957e397d9f7262ec5925586a
  label_registry_exact         registry  registry_exact          immutable  0.25.0
  label_registry_pessimistic   registry  registry_pessimistic    floating   0.25.0
  label_registry_unpinned      registry  registry_unconstrained  floating   0.25.0
  network                      local     local_path              local      ./modules/network
  version_metadata             local     local_path              local      ../modules/version-metadata

NOT REPRODUCIBLE: 5 floating module(s), 1 tag-pinned module(s), 4 unpinned provider(s)
```

and per module, e.g. the branch-tracking one resolved to a real commit:

```json
{
  "key": "label_git_branch",
  "kind": "git",
  "source": "git::https://github.com/cloudposse/terraform-null-label.git?ref=main",
  "repo": "github.com/cloudposse/terraform-null-label",
  "ref": "main",
  "ref_kind": "branch_or_tag",
  "resolved_commit": "38364d79c1082de13373666252e1276975919543",
  "resolved_commit_from": "checkout",
  "pin_kind": "git_branch",
  "pin_strength": "floating"
}
```

## Known limits (it's a PoC)

- **Everything is read at plan time from the working directory.** The metadata
  describes the machine doing the apply. It is accurate exactly because it reads
  what `init` resolved — but it means the working directory must be initialised
  (it degrades to empty output rather than failing if it isn't).
- **Constraint recovery is regex over HCL**, not a real parser. It anchors
  `version =` to a line start inside a `module` block, which is right for normal
  formatting but is not a substitute for `hcl.Parse`.
- **`ref_kind` guesses.** A branch literally named like a hex string reads as a
  short SHA. `resolved_commit` is authoritative either way.
- **Only top-level `*.tf` per module directory** is scanned (not nested
  subdirectories), matching how OpenTofu itself loads a module.
- OpenTofu downloads an identical module once and copies it for the other keys,
  and only the first copy keeps its `.git`. Copies inherit the commit from their
  twin (same source + same resolved version) and are marked
  `resolved_commit_from = "sibling_copy"`.
