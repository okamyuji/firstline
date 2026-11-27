variable "backup_bucket" {
  description = "Name of the S3 bucket Litestream replicates to."
  type        = string

  # The S3 bucket naming rules: 3 to 63 of a-z, 0-9, "." and "-", starting and ending
  # with a letter or digit, no "..". AWS itself rejects the rarer reserved forms.
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.backup_bucket)) && !strcontains(var.backup_bucket, "..")
    error_message = "backup_bucket must follow the S3 bucket naming rules."
  }
}

variable "alert_email" {
  description = "Address the alarms mail; SNS sends a confirmation link to it after the first apply."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", var.alert_email))
    error_message = "alert_email must be a mail address."
  }
}

# ADR-0003 measures the limit on the deployed host before sales open, so the first
# apply has none; the scale alarm appears on the apply that sets it.
variable "limit_load_items_per_sec" {
  description = "Limit load from the load test of ADR-0003, in sync items per second; the scale alarm fires at half of it."
  type        = number
  default     = null

  validation {
    condition     = var.limit_load_items_per_sec == null || coalesce(var.limit_load_items_per_sec, 1) > 0
    error_message = "limit_load_items_per_sec must be positive; measure it as ADR-0003 describes."
  }
}

variable "domain" {
  description = "Host name the DNS A record gives the Elastic IP; certbot gets its certificate and FIRSTLINE_BASE_URL is https://<domain>."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]{2,63}$", var.domain)) && length(var.domain) <= 253
    error_message = "domain must be a lowercase host name such as tickets.example.com."
  }
}

variable "firstline_version" {
  description = "Release tag the host installs at first boot, such as v0.1.0. Changing it later does not upgrade a running host; rerun firstline-install there."
  type        = string

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+$", var.firstline_version))
    error_message = "firstline_version must be a release tag such as v0.1.0."
  }
}

variable "firstline_sha256" {
  description = "SHA-256 of firstline-<version>-linux-arm64.tar.gz, from the release notes. Used at first boot only, like firstline_version."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.firstline_sha256))
    error_message = "firstline_sha256 must be 64 lowercase hex characters."
  }
}
