terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.108"
    }
  }
}

# Token is read from the DIGITALOCEAN_TOKEN environment variable.
provider "digitalocean" {}

locals {
  name = "my-app-prod"
  tags = ["vauban"]
  ssh_public_keys = [
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeployKeyDeployKeyDeployKeyDeployKey vauban-deploy",
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAuthorKeyAuthorKeyAuthorKeyAuthorKe me@laptop",
  ]
}

resource "digitalocean_ssh_key" "vauban" {
  count      = length(local.ssh_public_keys)
  name       = "${local.name}-${count.index}"
  public_key = local.ssh_public_keys[count.index]
}

resource "digitalocean_droplet" "vauban" {
  name       = local.name
  size       = "s-1vcpu-1gb"
  image      = "debian-13-x64"
  region     = "fra1"
  ssh_keys   = digitalocean_ssh_key.vauban[*].fingerprint
  user_data  = file("${path.module}/cloud-init.yaml")
  ipv6       = true
  monitoring = true
  tags       = local.tags

  # cloud-init and SSH keys only apply at first boot; changing them must not
  # destroy the running droplet.
  lifecycle {
    ignore_changes = [user_data, ssh_keys]
  }
}

resource "digitalocean_firewall" "vauban" {
  name        = local.name
  droplet_ids = [digitalocean_droplet.vauban.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "udp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

output "server_ip" {
  value = digitalocean_droplet.vauban.ipv4_address
}

output "server_ipv6" {
  value = digitalocean_droplet.vauban.ipv6_address
}
