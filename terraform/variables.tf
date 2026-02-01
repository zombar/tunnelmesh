variable "do_token" {
  description = "DigitalOcean API token"
  type        = string
  sensitive   = true
}

variable "domain" {
  description = "Base domain name (e.g., example.com)"
  type        = string
}

variable "subdomain" {
  description = "Subdomain for the coord server"
  type        = string
  default     = "tunnelmesh"
}

variable "auth_token" {
  description = "Authentication token for mesh peers"
  type        = string
  sensitive   = true
}

variable "mesh_cidr" {
  description = "CIDR block for mesh network IP allocation"
  type        = string
  default     = "10.99.0.0/16"
}

variable "domain_suffix" {
  description = "Domain suffix for mesh hostnames"
  type        = string
  default     = ".mesh"
}

variable "github_owner" {
  description = "GitHub username or organization for container image"
  type        = string
  default     = "zombar"
}

variable "image_tag" {
  description = "Docker image tag to deploy"
  type        = string
  default     = "latest"
}

variable "region" {
  description = "DigitalOcean region for deployment"
  type        = string
  default     = "ams"
}
