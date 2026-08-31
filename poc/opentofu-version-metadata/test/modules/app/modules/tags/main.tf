variable "name" {
  type = string
}

output "tags" {
  value = {
    Name      = var.name
    ManagedBy = "opentofu"
  }
}
