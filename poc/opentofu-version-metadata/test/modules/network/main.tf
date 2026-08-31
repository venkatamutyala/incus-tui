# Local module — versioned with the repo, nothing to resolve.
variable "cidr" {
  type    = string
  default = "10.0.0.0/16"
}

output "cidr" {
  value = var.cidr
}
