###############################################################################
# version-metadata — proof of concept
#
# Collects the versions that are ACTUALLY being applied for the whole root
# configuration and republishes them as outputs (so they land in the state
# file, where none of this metadata normally exists).
#
# Three sources, all read at plan time so the values are known before apply:
#
#   1. .terraform.lock.hcl            -> provider versions + constraints + hashes
#   2. .terraform/modules/modules.json -> every module actually installed
#                                         (resolved registry version, git ref,
#                                          local path, nested children)
#   3. the *.tf sources themselves     -> the DECLARED constraint per module
#                                         (`version = "~> 1.2"`), which the
#                                         manifest does not record
#
# Plus the CLI version (`tofu version`) via the external provider, because
# OpenTofu has no built-in `terraform.version` value.
#
# Deliberately one file: drop it in, call it once from the root module, done.
###############################################################################

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    # Only used to shell out for the CLI version; disable with
    # collect_cli_version = false if you do not want the dependency.
    external = {
      source  = "hashicorp/external"
      version = ">= 2.0.0"
    }
  }
}

###############################################################################
# Inputs — every one optional; the defaults work for a normal `tofu apply`.
###############################################################################

variable "root_path" {
  description = "Directory of the root module. Defaults to path.root. Override to point at a fixture directory in tests."
  type        = string
  default     = null
}

variable "lock_file" {
  description = "Path to the dependency lock file. Defaults to <root_path>/.terraform.lock.hcl."
  type        = string
  default     = null
}

variable "modules_manifest" {
  description = "Path to the installed-module manifest. Defaults to <root_path>/.terraform/modules/modules.json."
  type        = string
  default     = null
}

variable "scan_config_sources" {
  description = "Parse the *.tf files of the root and of every installed module to recover each module block's DECLARED source/version constraint. The manifest only records what was resolved, so without this you cannot tell a pinned registry module from a floating one."
  type        = bool
  default     = true
}

variable "resolve_git_commits" {
  description = "For git-sourced modules, read the checked-out .terraform/modules/<key>/.git to resolve the exact commit currently applied (turns `?ref=main` into a real SHA)."
  type        = bool
  default     = true
}

variable "collect_cli_version" {
  description = "Shell out to `tofu version` (via the external provider) to record which binary is applying this state."
  type        = bool
  default     = true
}

variable "warn_on_floating" {
  description = "Emit a check-block warning listing every module/provider that is not pinned to an immutable version."
  type        = bool
  default     = true
}

variable "extra_metadata" {
  description = "Free-form key/values merged into the published metadata (git sha of this repo, CI run id, ...)."
  type        = map(string)
  default     = {}
}

###############################################################################
# Stage 1 — locate and read the inputs
###############################################################################

locals {
  root          = var.root_path != null ? var.root_path : path.root
  lock_path     = var.lock_file != null ? var.lock_file : "${local.root}/.terraform.lock.hcl"
  manifest_path = var.modules_manifest != null ? var.modules_manifest : "${local.root}/.terraform/modules/modules.json"

  lock_raw     = fileexists(local.lock_path) ? file(local.lock_path) : ""
  manifest_raw = fileexists(local.manifest_path) ? file(local.manifest_path) : "{\"Modules\":[]}"

  manifest = try(jsondecode(local.manifest_raw), { Modules = [] })

  # Key "" is the root module itself — it has no source or version.
  manifest_entries = [
    for m in try(local.manifest.Modules, []) : {
      key     = m.Key
      source  = try(m.Source, "")
      version = try(m.Version, null) # resolved version; registry modules only
      dir     = try(m.Dir, null)
    }
    if try(m.Key, "") != ""
  ]
}

###############################################################################
# Stage 2 — providers, from the lock file
#
# Block shape:
#   provider "registry.opentofu.org/hashicorp/random" {
#     version     = "3.6.3"
#     constraints = "~> 3.6"
#     hashes = [ ... ]
#   }
# The lazy (.*?) stops at the first newline-anchored "}", which is the block
# close — the hashes list closes with an indented "]".
###############################################################################

locals {
  provider_blocks = regexall("(?s)provider\\s+\"([^\"]+)\"\\s*\\{(.*?)\\n\\}", local.lock_raw)

  providers_raw = {
    for b in local.provider_blocks : b[0] => {
      source_address = b[0]
      registry_host  = try(split("/", b[0])[0], null)
      namespace      = try(split("/", b[0])[1], null)
      type           = try(split("/", b[0])[2], null)
      version        = try(regex("version\\s*=\\s*\"([^\"]+)\"", b[1])[0], null)
      constraints    = try(regex("constraints\\s*=\\s*\"([^\"]+)\"", b[1])[0], null)
      hash_count     = length(regexall("\"(?:h1|zh|h0):", b[1]))
      # h1 hashes are the modern per-platform ones; zh-only means the lock was
      # written from the registry index without a local download.
      has_h1_hashes = can(regex("\"h1:", b[1]))
    }
  }
}

###############################################################################
# Stage 3 — declared constraints, by parsing the .tf sources
#
# modules.json records what was RESOLVED. Whether the caller wrote
# `version = "1.2.3"` or nothing at all only exists in the source, so scan it.
# Scanned per module directory, which makes nested children work too: a module
# block "tags" inside module "app" is recorded under the key "app.tags",
# exactly how the manifest keys it.
###############################################################################

locals {
  # key "" is the root module; every other key is an installed module's dir.
  scan_dirs = var.scan_config_sources ? merge(
    { "" = local.root },
    {
      for m in local.manifest_entries : m.key => (
        m.dir == null ? null : (startswith(m.dir, "/") ? m.dir : "${local.root}/${m.dir}")
      )
      if m.dir != null
    },
  ) : {}

  # try() because a module dir can vanish between init and plan, and because
  # fileset errors (rather than returning []) on a missing directory.
  tf_text = {
    for key, dir in local.scan_dirs : key => join("\n", [
      for f in try(tolist(fileset(dir, "*.tf")), []) : try(file("${dir}/${f}"), "")
    ])
  }

  # Root-level `terraform { required_version = ... }`.
  required_version_constraint = try(
    regex("required_version\\s*=\\s*\"([^\"]+)\"", local.tf_text[""])[0],
    null,
  )

  # Flatten every `module "x" { ... }` block found in every scanned directory
  # into the manifest's dotted key space.
  declared_list = flatten([
    for key, text in local.tf_text : [
      for b in regexall("(?s)module\\s+\"([^\"]+)\"\\s*\\{(.*?)\\n\\}", text) : {
        key    = key == "" ? b[0] : "${key}.${b[0]}"
        source = try(regex("source\\s*=\\s*\"([^\"]+)\"", b[1])[0], null)
        # Anchored to a line start so a `version` argument nested in an inner
        # block is far less likely to be picked up than the module's own.
        version = try(regex("(?m)^\\s*version\\s*=\\s*\"([^\"]+)\"", b[1])[0], null)
      }
    ]
  ])

  declared = { for d in local.declared_list : d.key => d }
}

###############################################################################
# Stage 4 — constraint classification
#
# OpenTofu has no user-defined functions, so classify once into a lookup map
# and share it between providers and modules instead of repeating the chain.
###############################################################################

locals {
  all_constraints = distinct(compact(concat(
    [for k, p in local.providers_raw : p.constraints == null ? "" : p.constraints],
    [for k, d in local.declared : d.version == null ? "" : d.version],
  )))

  constraint_kind = {
    for c in local.all_constraints : c => (
      # "1.2.3" or "= 1.2.3" and nothing else
      can(regex("^\\s*=?\\s*[0-9]+(\\.[0-9]+)*([-+][0-9A-Za-z.-]+)?\\s*$", c)) ? "exact" :
      strcontains(c, "~>") ? "pessimistic" :
      can(regex("[<>!]", c)) ? "range" : "other"
    )
  }
}

###############################################################################
# Stage 5 — modules: parse the source address of every installed module
###############################################################################

locals {
  module_parsed = {
    for m in local.manifest_entries : m.key => {
      key     = m.key
      source  = m.source
      version = m.version
      dir     = m.dir

      # "app.tags" -> depth 2, parent "app"
      depth  = length(split(".", m.key))
      parent = length(split(".", m.key)) > 1 ? join(".", slice(split(".", m.key), 0, length(split(".", m.key)) - 1)) : null

      # go-getter's forced type prefix, e.g. "git::" / "s3::" / "hg::"
      forced_type = try(regex("^([a-z0-9]+)::", m.source)[0], null)

      # source with the forced prefix, the query string and the URL scheme
      # stripped, so the remainder is host/path[//subdir]
      bare = replace(
        split("?", replace(m.source, "/^[a-z0-9]+::/", ""))[0],
        "/^[a-z0-9+.-]+:\\/\\//",
        "",
      )

      query = try(regex("\\?(.*)$", m.source)[0], null)
      ref   = try(regex("[?&]ref=([^&]+)", m.source)[0], null)
    }
  }

  module_rows = {
    for k, m in local.module_parsed : k => merge(m, {
      repo_path = split("//", m.bare)[0]
      subdir    = try(regex("//(.+)$", m.bare)[0], null)

      kind = (
        can(regex("^\\.{1,2}/", m.source)) || m.source == "." ? "local" :
        m.forced_type != null ? (m.forced_type == "git" ? "git" : m.forced_type) :
        can(regex("^git@", m.source)) ? "git" :
        can(regex("^(github\\.com|gitlab\\.com|bitbucket\\.org)/", m.source)) ? "git" :
        can(regex("^[a-z0-9+.-]+://", m.source)) ? "http" :
        can(regex("^([a-z0-9-]+\\.[a-z0-9.-]+/)?[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+", m.source)) ? "registry" :
        "unknown"
      )

      # NOTE: a branch literally named like a hex string (e.g. "deadbeef")
      # would be misread as a short SHA. Acceptable for a PoC; the resolved
      # commit below is the authoritative value either way.
      ref_kind = (
        m.ref == null ? "none" :
        can(regex("^[0-9a-f]{40}$", m.ref)) ? "commit_sha" :
        can(regex("^[0-9a-f]{7,39}$", m.ref)) ? "commit_sha_short" :
        can(regex("^v?[0-9]+\\.[0-9]+", m.ref)) ? "tag_semver" :
        "branch_or_tag"
      )

      declared_source     = try(local.declared[k].source, null)
      declared_version    = try(local.declared[k].version, null)
      declared_constraint = try(local.declared[k].version, null) == null ? "unconstrained" : lookup(local.constraint_kind, local.declared[k].version, "other")
    })
  }
}

###############################################################################
# Stage 6 — resolve git modules to the exact commit on disk
#
# `?ref=main` tells you nothing about what is being applied; the checkout does.
# go-getter leaves .git in place, so read HEAD — either a detached SHA, or a
# symbolic ref to chase through the loose ref file and then packed-refs.
###############################################################################

locals {
  # Registry modules are fetched from a VCS too, so their checkouts carry a
  # .git as well — resolving them turns a floating registry constraint into
  # the exact commit that was applied.
  git_dirs = var.resolve_git_commits ? {
    for k, m in local.module_rows : k => (startswith(m.dir, "/") ? m.dir : "${local.root}/${m.dir}")
    if contains(["git", "registry"], m.kind) && m.dir != null
  } : {}

  git_head = { for k, d in local.git_dirs : k => try(trimspace(file("${d}/.git/HEAD")), null) }

  git_head_commit = {
    for k, d in local.git_dirs : k => (
      local.git_head[k] == null ? null :
      # Detached HEAD: the file already holds the SHA.
      can(regex("^[0-9a-f]{40}$", local.git_head[k])) ? local.git_head[k] :
      # Symbolic HEAD: loose ref first, then packed-refs.
      try(
        trimspace(file("${d}/.git/${regex("^ref:\\s*(.+)$", local.git_head[k])[0]}")),
        regex("([0-9a-f]{40})\\s+${try(regex("^ref:\\s*(.+)$", local.git_head[k])[0], "")}\\b", file("${d}/.git/packed-refs"))[0],
        null,
      )
    )
  }

  # OpenTofu downloads an identical module once and copies it for the other
  # keys, and only the first copy keeps its .git. Two keys with the same source
  # AND the same resolved version are by definition the same commit, so let the
  # copies inherit it rather than reporting an unknown.
  commit_dedup_key = {
    for k, m in local.module_rows : k => "${m.source}@${m.version == null ? "" : m.version}"
  }

  commit_by_dedup_key = {
    for dk in distinct(values(local.commit_dedup_key)) : dk => try(
      [for k, c in local.git_head_commit : c if c != null && local.commit_dedup_key[k] == dk][0],
      null,
    )
  }

  git_commit = {
    for k, m in local.module_rows : k => (
      try(local.git_head_commit[k], null) != null
      ? local.git_head_commit[k]
      : try(local.commit_by_dedup_key[local.commit_dedup_key[k]], null)
    )
    if contains(keys(local.git_dirs), k) || try(local.commit_by_dedup_key[local.commit_dedup_key[k]], null) != null
  }

  # Where the commit came from, so an inherited value is never mistaken for a
  # direct observation of that checkout.
  git_commit_origin = {
    for k, c in local.git_commit : k => try(local.git_head_commit[k], null) != null ? "checkout" : "sibling_copy"
    if c != null
  }
}

###############################################################################
# Stage 7 — the published shape
###############################################################################

locals {
  providers_out = {
    for k, p in local.providers_raw : k => merge(p, {
      constraint_kind = p.constraints == null ? "unconstrained" : lookup(local.constraint_kind, p.constraints, "other")
      # A lock file always pins an exact version for the current run; this
      # flag is about whether the CONFIG lets that version move on re-lock.
      is_pinned = p.constraints != null && lookup(local.constraint_kind, p.constraints, "other") == "exact"
    })
  }

  modules_out = {
    for k, m in local.module_rows : k => {
      key    = m.key
      depth  = m.depth
      parent = m.parent
      kind   = m.kind
      source = m.source
      dir    = m.dir
      subdir = m.subdir

      # What is actually applied
      version              = m.version # registry: resolved version
      ref                  = m.ref     # git: the requested ref
      ref_kind             = m.ref_kind
      resolved_commit      = try(local.git_commit[k], null)        # what is actually checked out
      resolved_commit_from = try(local.git_commit_origin[k], null) # "checkout" | "sibling_copy"
      repo                 = m.kind == "git" ? trimsuffix(m.repo_path, ".git") : null

      # What the config asked for
      declared_source  = m.declared_source
      declared_version = m.declared_version
      constraint_kind  = m.kind == "registry" ? m.declared_constraint : null

      pin_kind = (
        m.kind == "local" ? "local_path" :
        m.kind == "registry" ? (m.declared_constraint == "exact" ? "registry_exact" : "registry_${m.declared_constraint}") :
        m.kind == "git" ? (
          m.ref_kind == "none" ? "git_default_branch" :
          m.ref_kind == "branch_or_tag" ? "git_branch" :
          m.ref_kind == "tag_semver" ? "git_tag" : "git_commit"
        ) :
        "other"
      )

      # immutable  = re-applying tomorrow provably gets the same bytes
      # mutable    = names a fixed point that someone could move (a git tag)
      # floating   = will drift on the next init/upgrade
      pin_strength = (
        m.kind == "local" ? "local" :
        m.kind == "registry" ? (m.declared_constraint == "exact" ? "immutable" : "floating") :
        m.kind == "git" ? (
          contains(["commit_sha", "commit_sha_short"], m.ref_kind) ? "immutable" :
          m.ref_kind == "tag_semver" ? "mutable" : "floating"
        ) :
        "unknown"
      )
    }
  }

  floating_modules = sort([for k, m in local.modules_out : k if m.pin_strength == "floating"])
  mutable_modules  = sort([for k, m in local.modules_out : k if m.pin_strength == "mutable"])
  floating_providers = sort([
    for k, p in local.providers_out : k if !p.is_pinned
  ])

  cli_line = var.collect_cli_version ? try(trimspace(data.external.cli[0].result.version_line), "") : ""

  cli = {
    product                     = try(regex("^(\\S+)", local.cli_line)[0], null)
    version                     = try(regex("v?([0-9]+\\.[0-9]+\\.[0-9]+\\S*)", local.cli_line)[0], null)
    version_line                = local.cli_line == "" ? null : local.cli_line
    required_version_constraint = local.required_version_constraint
    workspace                   = terraform.workspace
  }

  summary = {
    module_count   = length(local.modules_out)
    provider_count = length(local.providers_out)

    modules_by_kind = {
      for kind in distinct([for k, m in local.modules_out : m.kind]) :
      kind => length([for k, m in local.modules_out : k if m.kind == kind])
    }
    modules_by_pin_strength = {
      for s in distinct([for k, m in local.modules_out : m.pin_strength]) :
      s => length([for k, m in local.modules_out : k if m.pin_strength == s])
    }
    providers_by_constraint_kind = {
      for c in distinct([for k, p in local.providers_out : p.constraint_kind]) :
      c => length([for k, p in local.providers_out : k if p.constraint_kind == c])
    }

    floating_module_count   = length(local.floating_modules)
    mutable_module_count    = length(local.mutable_modules)
    floating_provider_count = length(local.floating_providers)
  }

  metadata = {
    cli       = local.cli
    summary   = local.summary
    providers = local.providers_out
    modules   = local.modules_out
    extra     = var.extra_metadata
  }

  # Human-readable, for `tofu output -raw report`.
  report = join("\n", concat(
    [
      "CLI            : ${coalesce(local.cli.version_line, "unknown")}",
      "required_version: ${coalesce(local.required_version_constraint, "(none)")}",
      "workspace      : ${terraform.workspace}",
      "",
      "PROVIDERS (${local.summary.provider_count})",
    ],
    [
      for k in sort(keys(local.providers_out)) :
      format("  %-52s %-10s %-12s %s", k, coalesce(local.providers_out[k].version, "?"), local.providers_out[k].constraint_kind, coalesce(local.providers_out[k].constraints, "(unconstrained)"))
    ],
    [
      "",
      "MODULES (${local.summary.module_count})",
    ],
    [
      for k in sort(keys(local.modules_out)) :
      format("  %-28s %-9s %-23s %-10s %s", k, local.modules_out[k].kind, local.modules_out[k].pin_kind, local.modules_out[k].pin_strength,
        coalesce(
          local.modules_out[k].version,
          try(local.modules_out[k].resolved_commit, null),
          local.modules_out[k].ref,
          local.modules_out[k].source,
      ))
    ],
    [
      "",
      "NOT REPRODUCIBLE: ${local.summary.floating_module_count} floating module(s), ${local.summary.mutable_module_count} tag-pinned module(s), ${local.summary.floating_provider_count} unpinned provider(s)",
    ],
  ))
}

###############################################################################
# CLI version — OpenTofu exposes no `terraform.version`, so ask the binary.
# Runs at plan time; printf always emits valid JSON even if no binary is found.
###############################################################################

data "external" "cli" {
  count = var.collect_cli_version ? 1 : 0

  program = ["/bin/sh", "-c", <<-EOT
    line="$( (tofu version 2>/dev/null || terraform version 2>/dev/null) | head -n 1 )"
    printf '{"version_line":"%s"}' "$line"
  EOT
  ]
}

###############################################################################
# Advisory only — a check block warns, it never fails the plan.
###############################################################################

check "dependencies_are_reproducible" {
  assert {
    condition = !var.warn_on_floating || (
      length(local.floating_modules) == 0 && length(local.floating_providers) == 0
    )
    error_message = join(" ", [
      "Some dependencies are not pinned to an immutable version:",
      "modules=[${join(", ", local.floating_modules)}]",
      "providers=[${join(", ", local.floating_providers)}].",
      "Re-running init could resolve different code.",
    ])
  }
}

###############################################################################
# Outputs — this is the point: none of it is in the state until published here.
###############################################################################

output "metadata" {
  description = "Everything, as one object: cli, summary, providers, modules, extra."
  value       = local.metadata
}

output "metadata_json" {
  description = "The same object as a JSON string — the easiest thing to diff between applies or ship to an inventory."
  value       = jsonencode(local.metadata)
}

output "fingerprint" {
  description = "sha256 of metadata_json. Changes if any resolved version, ref or commit moved."
  value       = sha256(jsonencode(local.metadata))
}

output "cli" {
  description = "Which binary is applying this state, and the root's required_version constraint."
  value       = local.cli
}

output "providers" {
  description = "Provider versions and constraints, keyed by source address, from the lock file."
  value       = local.providers_out
}

output "modules" {
  description = "Every installed module, keyed as the manifest keys them (children are dotted, e.g. app.tags)."
  value       = local.modules_out
}

output "summary" {
  description = "Counts by kind, pin strength and constraint kind."
  value       = local.summary
}

output "not_reproducible" {
  description = "The actionable list: dependencies that could resolve differently on the next init."
  value = {
    floating_modules   = local.floating_modules
    mutable_modules    = local.mutable_modules
    floating_providers = local.floating_providers
  }
}

output "report" {
  description = "Human-readable table. `tofu output -raw report`."
  value       = local.report
}
