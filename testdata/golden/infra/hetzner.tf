terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.70"
    }
  }
}

# Token is read from the HCLOUD_TOKEN environment variable.
provider "hcloud" {}

locals {
  name = "my-app-prod"
  labels = {
    app        = local.name
    managed-by = "vauban"
  }
  ssh_public_keys = [
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployKeyDeployKeyDeployKeyDeployKey vauban-deploy",
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorKeyAuthorKeyAuthorKeyAuthorKe me@laptop",
  ]
}

resource "hcloud_ssh_key" "vauban" {
  count      = length(local.ssh_public_keys)
  name       = "${local.name}-${count.index}"
  public_key = local.ssh_public_keys[count.index]
  labels     = local.labels
}

resource "hcloud_firewall" "vauban" {
  name   = local.name
  labels = local.labels

  rule {
    description = "ssh"
    direction   = "in"
    protocol    = "tcp"
    port        = "22"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }

  rule {
    description = "http"
    direction   = "in"
    protocol    = "tcp"
    port        = "80"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }

  rule {
    description = "https"
    direction   = "in"
    protocol    = "tcp"
    port        = "443"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }

  rule {
    description = "http3"
    direction   = "in"
    protocol    = "udp"
    port        = "443"
    source_ips  = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_server" "vauban" {
  name         = local.name
  server_type  = "cx23"
  image        = "debian-13"
  location     = "nbg1"
  ssh_keys     = hcloud_ssh_key.vauban[*].id
  firewall_ids = [hcloud_firewall.vauban.id]
  user_data    = file("${path.module}/cloud-init.yaml")
  labels       = local.labels

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  # cloud-init and SSH keys only apply at first boot; changing them must not
  # destroy the running server.
  lifecycle {
    ignore_changes = [user_data, ssh_keys]
  }
}

output "server_ip" {
  value = hcloud_server.vauban.ipv4_address
}

output "server_ipv6" {
  value = hcloud_server.vauban.ipv6_address
}
