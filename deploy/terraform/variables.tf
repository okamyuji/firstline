variable "backup_bucket" {
  description = "Name of the S3 bucket Litestream replicates to."
  type        = string
}

variable "alert_email" {
  description = "Address the alarms mail; SNS sends a confirmation link to it after the first apply."
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.alert_email))
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
