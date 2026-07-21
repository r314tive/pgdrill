output "owner_user" {
  description = "Bootstrap operator login created on every VM."
  value       = var.owner_user
}

output "admin_users" {
  description = "Dedicated non-owner administrator logins created on every VM."
  value       = sort(keys(var.admin_ssh_public_key_paths))
}

output "admin_access" {
  description = "Dedicated administrator SSH destinations and fixed-command entry points."
  value = {
    for login in sort(keys(var.admin_ssh_public_key_paths)) : login => {
      runner = "ssh ${login}@${yandex_compute_instance.runner.network_interface[0].nat_ip_address}"
      source = "ssh -J ${login}@${yandex_compute_instance.runner.network_interface[0].nat_ip_address} ${login}@${local.source_ip}"
      runner_commands = [
        "sudo -u postgres /usr/local/sbin/pgdrill-demo-doctor",
        "sudo -u postgres /usr/local/sbin/pgdrill-demo-run",
        "sudo -u postgres /usr/local/sbin/pgdrill-demo-report",
      ]
      source_command = "sudo -u postgres /usr/local/sbin/pgdrill-demo-source-status"
    }
  }
}

output "runner_public_ip" {
  description = "Only public VM address in the demo topology."
  value       = yandex_compute_instance.runner.network_interface[0].nat_ip_address
}

output "runner_private_ip" {
  value = local.runner_ip
}

output "source_private_ip" {
  value = local.source_ip
}

output "repository_private_ip" {
  value = local.repository_ip
}

output "ssh_owner_runner" {
  description = "Owner SSH destination. Add the appropriate IdentityFile locally."
  value       = "ssh ${var.owner_user}@${yandex_compute_instance.runner.network_interface[0].nat_ip_address}"
}

output "ssh_owner_source" {
  description = "Owner SSH destination through the runner bastion."
  value       = "ssh -J ${var.owner_user}@${yandex_compute_instance.runner.network_interface[0].nat_ip_address} ${var.owner_user}@${local.source_ip}"
}

output "demo_inventory" {
  description = "Secret-free topology inventory for the operator runbook."
  value = {
    image_id          = data.yandex_compute_image.ubuntu.id
    image_family      = var.image_family
    platform_id       = var.platform_id
    preemptible       = var.preemptible
    repository_ip     = local.repository_ip
    runner_private_ip = local.runner_ip
    runner_public_ip  = yandex_compute_instance.runner.network_interface[0].nat_ip_address
    source_ip         = local.source_ip
    zone              = var.zone
  }
}
