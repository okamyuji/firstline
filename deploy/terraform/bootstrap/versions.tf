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
}
