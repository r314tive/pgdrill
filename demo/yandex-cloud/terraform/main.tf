locals {
  source_name     = "${var.name_prefix}-source"
  repository_name = "${var.name_prefix}-repository"
  runner_name     = "${var.name_prefix}-runner"

  source_ip     = cidrhost(var.subnet_cidr, 10)
  repository_ip = cidrhost(var.subnet_cidr, 20)
  runner_ip     = cidrhost(var.subnet_cidr, 30)
  postgres_uid  = 2000
  postgres_gid  = 2000

  admin_ingress_cidrs = distinct([
    for cidr in var.admin_ingress_cidrs : trimspace(cidr)
  ])

  owner_ssh_public_key = trimspace(file(pathexpand(var.owner_ssh_public_key_path)))
  admin_ssh_public_keys = {
    for login, path in var.admin_ssh_public_key_paths :
    login => trimspace(file(pathexpand(path)))
  }

  owner_cloud_init_user = {
    name                = var.owner_user
    gecos               = "pgdrill demo owner"
    groups              = ["sudo", "pgdrill-demo-admins"]
    shell               = "/bin/bash"
    lock_passwd         = true
    sudo                = ["ALL=(ALL) NOPASSWD:ALL"]
    ssh_authorized_keys = [local.owner_ssh_public_key]
  }
  admin_cloud_init_users = [
    for login, key in local.admin_ssh_public_keys : {
      name                = login
      gecos               = "pgdrill demo administrator"
      groups              = ["pgdrill-demo-admins"]
      shell               = "/bin/bash"
      lock_passwd         = true
      sudo                = []
      ssh_authorized_keys = [key]
    }
  ]
  cloud_init_users = concat(
    [local.owner_cloud_init_user],
    local.admin_cloud_init_users,
  )

  labels = {
    application = "pgdrill"
    environment = "demo"
    managed_by  = "terraform"
  }

  source_cloud_init = templatefile("${path.module}/cloud-init/source.yaml.tftpl", {
    hostname      = local.source_name
    repository_ip = local.repository_ip
    users_yaml    = yamlencode(local.cloud_init_users)
  })
  repository_cloud_init = templatefile("${path.module}/cloud-init/repository.yaml.tftpl", {
    hostname     = local.repository_name
    source_ip    = local.source_ip
    runner_ip    = local.runner_ip
    postgres_uid = local.postgres_uid
    postgres_gid = local.postgres_gid
    users_yaml   = yamlencode([local.owner_cloud_init_user])
  })
  runner_cloud_init = templatefile("${path.module}/cloud-init/runner.yaml.tftpl", {
    hostname      = local.runner_name
    repository_ip = local.repository_ip
    users_yaml    = yamlencode(local.cloud_init_users)
  })
}

resource "terraform_data" "access_revision" {
  input = sha256(jsonencode({
    owner_user            = var.owner_user
    owner_ssh_public_key  = local.owner_ssh_public_key
    admin_ssh_public_keys = local.admin_ssh_public_keys
  }))

  lifecycle {
    precondition {
      condition     = !contains(keys(var.admin_ssh_public_key_paths), var.owner_user)
      error_message = "owner_user must not also appear in admin_ssh_public_key_paths."
    }

    precondition {
      condition = alltrue([
        for key in concat([local.owner_ssh_public_key], values(local.admin_ssh_public_keys)) :
        can(regex("^(ssh-|ecdsa-|sk-)[^[:space:]]+[[:space:]]+[A-Za-z0-9+/=]+", key))
      ])
      error_message = "Every SSH key path must contain an OpenSSH public key, never a private key."
    }
  }
}

resource "terraform_data" "owner_access_revision" {
  input = sha256(jsonencode({
    owner_user           = var.owner_user
    owner_ssh_public_key = local.owner_ssh_public_key
  }))
}

check "cloud_init_documents" {
  assert {
    condition = alltrue([
      length(try(yamldecode(local.source_cloud_init).users, [])) == length(local.cloud_init_users),
      length(try(yamldecode(local.repository_cloud_init).users, [])) == 1,
      length(try(yamldecode(local.runner_cloud_init).users, [])) == length(local.cloud_init_users),
    ])
    error_message = "Every rendered cloud-init document must be valid YAML and contain the complete user list."
  }

  assert {
    condition = (
      can(regex("root_squash", local.repository_cloud_init)) &&
      !can(regex("all_squash", local.repository_cloud_init)) &&
      can(regex("nfs4 rw,", local.source_cloud_init)) &&
      can(regex("nfs4 ro,", local.runner_cloud_init))
    )
    error_message = "Cloud-init must retain UID-scoped NFS exports, a read-write source mount, and a read-only runner mount."
  }
}

data "yandex_compute_image" "ubuntu" {
  family    = var.image_family
  folder_id = "standard-images"
}

resource "yandex_vpc_network" "demo" {
  name   = "${var.name_prefix}-network"
  labels = local.labels
}

resource "yandex_vpc_gateway" "egress" {
  name      = "${var.name_prefix}-egress"
  folder_id = var.folder_id
  labels    = local.labels

  shared_egress_gateway {}
}

resource "yandex_vpc_route_table" "private_egress" {
  name       = "${var.name_prefix}-private-egress"
  network_id = yandex_vpc_network.demo.id
  labels     = local.labels

  static_route {
    destination_prefix = "0.0.0.0/0"
    gateway_id         = yandex_vpc_gateway.egress.id
  }
}

resource "yandex_vpc_subnet" "demo" {
  name           = "${var.name_prefix}-subnet"
  zone           = var.zone
  network_id     = yandex_vpc_network.demo.id
  v4_cidr_blocks = [var.subnet_cidr]
  route_table_id = yandex_vpc_route_table.private_egress.id
  labels         = local.labels
}

resource "yandex_vpc_security_group" "runner" {
  name       = "${var.name_prefix}-runner"
  network_id = yandex_vpc_network.demo.id
  labels     = local.labels

  ingress {
    description    = "SSH from explicitly trusted administrator networks"
    protocol       = "TCP"
    port           = 22
    v4_cidr_blocks = local.admin_ingress_cidrs
  }

  egress {
    description    = "Outbound package, release, and private-service access"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "yandex_vpc_security_group" "source" {
  name       = "${var.name_prefix}-source"
  network_id = yandex_vpc_network.demo.id
  labels     = local.labels

  ingress {
    description       = "SSH through the runner bastion"
    protocol          = "TCP"
    port              = 22
    security_group_id = yandex_vpc_security_group.runner.id
  }

  ingress {
    description       = "Private diagnostics from the runner"
    protocol          = "ICMP"
    security_group_id = yandex_vpc_security_group.runner.id
  }

  egress {
    description    = "Outbound package and repository access"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "yandex_vpc_security_group" "repository" {
  name       = "${var.name_prefix}-repository"
  network_id = yandex_vpc_network.demo.id
  labels     = local.labels

  ingress {
    description       = "SSH through the runner bastion"
    protocol          = "TCP"
    port              = 22
    security_group_id = yandex_vpc_security_group.runner.id
  }

  ingress {
    description       = "NFSv4 writes from the source"
    protocol          = "TCP"
    port              = 2049
    security_group_id = yandex_vpc_security_group.source.id
  }

  ingress {
    description       = "Read-only NFSv4 access from the runner"
    protocol          = "TCP"
    port              = 2049
    security_group_id = yandex_vpc_security_group.runner.id
  }

  ingress {
    description       = "Private diagnostics from the runner"
    protocol          = "ICMP"
    security_group_id = yandex_vpc_security_group.runner.id
  }

  egress {
    description    = "Outbound package and response traffic"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "yandex_compute_instance" "repository" {
  name                      = local.repository_name
  hostname                  = local.repository_name
  platform_id               = var.platform_id
  zone                      = var.zone
  allow_stopping_for_update = true
  labels                    = merge(local.labels, { role = "repository" })

  resources {
    cores         = var.vm_profiles.repository.cores
    memory        = var.vm_profiles.repository.memory
    core_fraction = var.vm_profiles.repository.core_fraction
  }

  scheduling_policy {
    preemptible = var.preemptible
  }

  boot_disk {
    auto_delete = true

    initialize_params {
      image_id = data.yandex_compute_image.ubuntu.id
      size     = var.vm_profiles.repository.disk_size
      type     = var.vm_profiles.repository.disk_type
    }
  }

  network_interface {
    subnet_id          = yandex_vpc_subnet.demo.id
    ip_address         = local.repository_ip
    nat                = false
    security_group_ids = [yandex_vpc_security_group.repository.id]
  }

  metadata = {
    "serial-port-enable" = "0"
    "user-data"          = local.repository_cloud_init
  }

  lifecycle {
    replace_triggered_by = [terraform_data.owner_access_revision]
  }
}

resource "yandex_compute_instance" "source" {
  name                      = local.source_name
  hostname                  = local.source_name
  platform_id               = var.platform_id
  zone                      = var.zone
  allow_stopping_for_update = true
  labels                    = merge(local.labels, { role = "source" })

  resources {
    cores         = var.vm_profiles.source.cores
    memory        = var.vm_profiles.source.memory
    core_fraction = var.vm_profiles.source.core_fraction
  }

  scheduling_policy {
    preemptible = var.preemptible
  }

  boot_disk {
    auto_delete = true

    initialize_params {
      image_id = data.yandex_compute_image.ubuntu.id
      size     = var.vm_profiles.source.disk_size
      type     = var.vm_profiles.source.disk_type
    }
  }

  network_interface {
    subnet_id          = yandex_vpc_subnet.demo.id
    ip_address         = local.source_ip
    nat                = false
    security_group_ids = [yandex_vpc_security_group.source.id]
  }

  metadata = {
    "serial-port-enable" = "0"
    "user-data"          = local.source_cloud_init
  }

  lifecycle {
    replace_triggered_by = [terraform_data.access_revision]
  }

  depends_on = [yandex_compute_instance.repository]
}

resource "yandex_compute_instance" "runner" {
  name                      = local.runner_name
  hostname                  = local.runner_name
  platform_id               = var.platform_id
  zone                      = var.zone
  allow_stopping_for_update = true
  labels                    = merge(local.labels, { role = "runner" })

  resources {
    cores         = var.vm_profiles.runner.cores
    memory        = var.vm_profiles.runner.memory
    core_fraction = var.vm_profiles.runner.core_fraction
  }

  scheduling_policy {
    preemptible = var.preemptible
  }

  boot_disk {
    auto_delete = true

    initialize_params {
      image_id = data.yandex_compute_image.ubuntu.id
      size     = var.vm_profiles.runner.disk_size
      type     = var.vm_profiles.runner.disk_type
    }
  }

  network_interface {
    subnet_id          = yandex_vpc_subnet.demo.id
    ip_address         = local.runner_ip
    nat                = true
    security_group_ids = [yandex_vpc_security_group.runner.id]
  }

  metadata = {
    "serial-port-enable" = "0"
    "user-data"          = local.runner_cloud_init
  }

  lifecycle {
    replace_triggered_by = [terraform_data.access_revision]
  }

  depends_on = [yandex_compute_instance.repository]
}
