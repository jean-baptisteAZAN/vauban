terraform {
  required_providers {
    scaleway = {
      source  = "scaleway/scaleway"
      version = "~> 2.92"
    }
  }
}

# Credentials and project come from the SCW_ACCESS_KEY, SCW_SECRET_KEY and
# SCW_DEFAULT_PROJECT_ID environment variables.
provider "scaleway" {
  zone   = "fr-par-1"
  region = "fr-par"
}

# SSH keys reach the instance through cloud-init only: Scaleway IAM SSH keys
# are project-wide and would be installed on every instance of the project.
locals {
  name = "my-app-prod"
  tags = ["vauban"]
}

resource "scaleway_instance_ip" "ipv4" {
  type = "routed_ipv4"
  tags = local.tags
}

resource "scaleway_instance_ip" "ipv6" {
  type = "routed_ipv6"
  tags = local.tags
}

resource "scaleway_instance_security_group" "vauban" {
  name                    = local.name
  inbound_default_policy  = "drop"
  outbound_default_policy = "accept"
  tags                    = local.tags

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 22
    ip_range = "0.0.0.0/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 22
    ip_range = "::/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 80
    ip_range = "0.0.0.0/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 80
    ip_range = "::/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 443
    ip_range = "0.0.0.0/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 443
    ip_range = "::/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "UDP"
    port     = 443
    ip_range = "0.0.0.0/0"
  }

  inbound_rule {
    action   = "accept"
    protocol = "UDP"
    port     = 443
    ip_range = "::/0"
  }
}

resource "scaleway_instance_server" "vauban" {
  name              = local.name
  type              = "PLAY2-PICO"
  image             = "debian_trixie"
  ip_ids            = [scaleway_instance_ip.ipv4.id, scaleway_instance_ip.ipv6.id]
  security_group_id = scaleway_instance_security_group.vauban.id
  tags              = local.tags

  user_data = {
    cloud-init = file("${path.module}/cloud-init.yaml")
  }

  # cloud-init only applies at first boot; changing it must not replace the
  # running instance.
  lifecycle {
    ignore_changes = [user_data]
  }
}

output "server_ip" {
  value = scaleway_instance_ip.ipv4.address
}

# The ipv6 resource only exposes the prefix; read the full address from the server.
output "server_ipv6" {
  value = one([for ip in scaleway_instance_server.vauban.public_ips : ip.address if ip.family == "inet6"])

  precondition {
    condition     = length([for ip in scaleway_instance_server.vauban.public_ips : ip if ip.family == "inet6"]) == 1
    error_message = "the server has no public IPv6 address; expected exactly one."
  }
}
