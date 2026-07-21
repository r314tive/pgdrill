variable "cloud_id" {
  description = "Yandex Cloud ID containing the disposable demo environment."
  type        = string

  validation {
    condition     = length(trimspace(var.cloud_id)) > 0
    error_message = "cloud_id must not be empty."
  }
}

variable "folder_id" {
  description = "Yandex Cloud folder ID containing the disposable demo environment."
  type        = string

  validation {
    condition     = length(trimspace(var.folder_id)) > 0
    error_message = "folder_id must not be empty."
  }
}

variable "zone" {
  description = "Availability zone for all demo resources."
  type        = string
  default     = "ru-central1-a"

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.zone))
    error_message = "zone must be a valid Yandex Cloud zone name."
  }
}

variable "name_prefix" {
  description = "Prefix used for disposable demo resource names."
  type        = string
  default     = "pgdrill-demo"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,39}$", var.name_prefix))
    error_message = "name_prefix must be 3-40 lowercase letters, digits, or hyphens and start with a letter."
  }
}

variable "subnet_cidr" {
  description = "Private IPv4 /24 used by the three demo VMs."
  type        = string
  default     = "10.42.0.0/24"

  validation {
    condition     = can(cidrnetmask(var.subnet_cidr)) && endswith(var.subnet_cidr, "/24")
    error_message = "subnet_cidr must be a valid IPv4 /24."
  }
}

variable "admin_ingress_cidrs" {
  description = "Trusted public IPv4 CIDRs allowed to SSH to the runner VM. A world-open rule is rejected."
  type        = list(string)

  validation {
    condition = length(var.admin_ingress_cidrs) > 0 && alltrue([
      for cidr in var.admin_ingress_cidrs :
      can(cidrnetmask(trimspace(cidr))) &&
      can(regex("/(1[6-9]|2[0-9]|3[0-2])$", trimspace(cidr)))
    ])
    error_message = "admin_ingress_cidrs must contain valid IPv4 CIDRs with /16 through /32 prefixes."
  }
}

variable "owner_user" {
  description = "Bootstrap operator account with sudo on every VM."
  type        = string
  default     = "pgdrill-owner"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,30}$", var.owner_user)) && !contains(["root", "postgres"], var.owner_user)
    error_message = "owner_user must be a safe Linux login other than root or postgres."
  }
}

variable "owner_ssh_public_key_path" {
  description = "Local path to the owner's OpenSSH public key. Do not provide a private key."
  type        = string

  validation {
    condition     = length(trimspace(var.owner_ssh_public_key_path)) > 0
    error_message = "owner_ssh_public_key_path must not be empty."
  }
}

variable "admin_ssh_public_key_paths" {
  description = "Map of dedicated administrator login names to local OpenSSH public-key paths."
  type        = map(string)
  default     = {}

  validation {
    condition = length(var.admin_ssh_public_key_paths) <= 32 && alltrue([
      for login, path in var.admin_ssh_public_key_paths :
      can(regex("^[a-z][a-z0-9-]{0,30}$", login)) &&
      !contains(["root", "postgres"], login) &&
      length(trimspace(path)) > 0
    ])
    error_message = "At most 32 administrator logins may be configured; names must be safe and unique, and each public-key path must be non-empty."
  }
}

variable "image_family" {
  description = "Yandex Cloud public image family used for all VMs."
  type        = string
  default     = "ubuntu-2404-lts"
}

variable "platform_id" {
  description = "Compute platform used for all VMs."
  type        = string
  default     = "standard-v3"
}

variable "preemptible" {
  description = "Use preemptible VMs. Keep false for a scheduled customer demo."
  type        = bool
  default     = false
}

variable "vm_profiles" {
  description = "Per-role VM sizing. Disk sizes are in GiB and memory is in GiB."
  type = map(object({
    cores         = number
    memory        = number
    core_fraction = number
    disk_size     = number
    disk_type     = string
  }))

  default = {
    source = {
      cores         = 2
      memory        = 4
      core_fraction = 100
      disk_size     = 30
      disk_type     = "network-ssd"
    }
    repository = {
      cores         = 2
      memory        = 2
      core_fraction = 20
      disk_size     = 80
      disk_type     = "network-hdd"
    }
    runner = {
      cores         = 2
      memory        = 4
      core_fraction = 100
      disk_size     = 40
      disk_type     = "network-ssd"
    }
  }

  validation {
    condition = length(var.vm_profiles) == 3 && alltrue([
      for role in ["source", "repository", "runner"] : contains(keys(var.vm_profiles), role)
      ]) && alltrue([
      for profile in values(var.vm_profiles) :
      profile.cores >= 2 &&
      profile.memory >= 2 &&
      contains([20, 50, 100], profile.core_fraction) &&
      profile.disk_size >= 20 &&
      contains(["network-hdd", "network-ssd"], profile.disk_type)
    ])
    error_message = "vm_profiles must define source, repository, and runner with supported, non-trivial resource values."
  }
}
