# Run once with local state to create the bucket that holds the state of ../.
terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.74"
    }
  }
}

provider "aws" {
  region = "ap-northeast-1"
}

variable "state_bucket" {
  description = "Name of the S3 bucket for the terraform state of deploy/terraform."
  type        = string

  # The S3 bucket naming rules: 3 to 63 of a-z, 0-9, "." and "-", starting and ending
  # with a letter or digit, no "..". AWS itself rejects the rarer reserved forms.
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.state_bucket)) && !strcontains(var.state_bucket, "..")
    error_message = "state_bucket must follow the S3 bucket naming rules."
  }
}
