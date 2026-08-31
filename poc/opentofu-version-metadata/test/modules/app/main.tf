# Local module that itself calls a child, so the manifest records a nested
# key ("app.tags") and the metadata module has a depth-2 row to report.
variable "name" {
  type    = string
  default = "demo"
}

module "tags" {
  source = "./modules/tags"
  name   = var.name
}

output "tags" {
  value = module.tags.tags
}
