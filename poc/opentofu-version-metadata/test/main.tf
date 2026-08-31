###############################################################################
# Test situation for the version-metadata module.
#
# One state file, ten modules, deliberately mixed:
#
#   #   module                        source            pinning
#   --- ----------------------------- ----------------- -------------------------
#   1   label_registry_exact          TF registry       version = "0.25.0"
#   2   label_registry_pessimistic    TF registry       version = "~> 0.25.0"
#   3   label_registry_unpinned       TF registry       (no version at all)
#   4   dir_template_range            TF registry       version = ">= 1.0.0, < 2.0.0"
#   5   label_git_tag                 GitHub (git::)    ?ref=0.25.0        (tag)
#   6   label_git_commit              GitHub (git::)    ?ref=488ab91...    (commit SHA)
#   7   label_git_branch              GitHub (git::)    ?ref=main          (branch)
#   8   label_git_default             GitHub shorthand  (no ref -> default branch)
#   9   network                       local path        ./modules/network
#   10  app                           local path        ./modules/app (nests app.tags)
#
# and four providers pinned four different ways (plus `external`, pulled in by
# the metadata module itself with a >= constraint).
#
# Modules 1-3 and 5-8 point at the same small upstream repo on purpose: the
# thing under test is how each source/pin style is recorded, and one small
# dependency keeps `tofu init` fast and reliable.
###############################################################################

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    # exact pin
    null = {
      source  = "hashicorp/null"
      version = "3.2.4"
    }
    # pessimistic pin
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    # range
    local = {
      source  = "hashicorp/local"
      version = ">= 2.4.0, < 3.0.0"
    }
    # completely unpinned
    time = {
      source = "hashicorp/time"
    }
  }
}

########################################
# 1-4: Terraform/OpenTofu registry
########################################

module "label_registry_exact" {
  source  = "cloudposse/label/null"
  version = "0.25.0"

  namespace = "poc"
  name      = "exact"
}

module "label_registry_pessimistic" {
  source  = "cloudposse/label/null"
  version = "~> 0.25.0"

  namespace = "poc"
  name      = "pessimistic"
}

module "label_registry_unpinned" {
  source = "cloudposse/label/null"

  namespace = "poc"
  name      = "unpinned"
}

module "dir_template_range" {
  source  = "hashicorp/dir/template"
  version = ">= 1.0.0, < 2.0.0"

  base_dir = "${path.module}/modules/network"
}

########################################
# 5-8: GitHub
########################################

module "label_git_tag" {
  source = "git::https://github.com/cloudposse/terraform-null-label.git?ref=0.25.0"

  namespace = "poc"
  name      = "git-tag"
}

module "label_git_commit" {
  source = "git::https://github.com/cloudposse/terraform-null-label.git?ref=488ab91e34a24a86957e397d9f7262ec5925586a"

  namespace = "poc"
  name      = "git-commit"
}

module "label_git_branch" {
  source = "git::https://github.com/cloudposse/terraform-null-label.git?ref=main"

  namespace = "poc"
  name      = "git-branch"
}

module "label_git_default" {
  source = "github.com/cloudposse/terraform-null-label"

  namespace = "poc"
  name      = "git-default"
}

########################################
# 9-10: local paths
########################################

module "network" {
  source = "./modules/network"
  cidr   = "10.42.0.0/16"
}

module "app" {
  source = "./modules/app"
  name   = "poc-app"
}

########################################
# A few resources so the state is real
########################################

resource "random_pet" "example" {
  length = 2
}

resource "null_resource" "example" {
  triggers = {
    pet = random_pet.example.id
  }
}

resource "time_static" "example" {}

########################################
# The module under test
########################################

module "version_metadata" {
  source = "../modules/version-metadata"

  extra_metadata = {
    component = "incus-tui-poc"
    owner     = "platform"
  }
}

########################################
# Republish into the state file
########################################

output "dependency_metadata" {
  description = "The whole inventory — providers, modules, refs, resolved commits."
  value       = module.version_metadata.metadata
}

output "dependency_fingerprint" {
  value = module.version_metadata.fingerprint
}

output "not_reproducible" {
  value = module.version_metadata.not_reproducible
}

output "dependency_report" {
  value = module.version_metadata.report
}
