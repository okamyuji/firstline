mock_provider "aws" {}

variables {
  state_bucket = "firstline-tfstate-test"
}

run "state_bucket_is_versioned_and_private" {
  command = apply

  assert {
    condition     = aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled"
    error_message = "versioning keeps earlier states when an apply writes a bad one"
  }
  assert {
    condition     = aws_s3_bucket.state.force_destroy == false
    error_message = "force_destroy would let terraform destroy delete every state"
  }
  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.state.block_public_acls,
      aws_s3_bucket_public_access_block.state.block_public_policy,
      aws_s3_bucket_public_access_block.state.ignore_public_acls,
      aws_s3_bucket_public_access_block.state.restrict_public_buckets,
    ])
    error_message = "all four public access blocks must be on"
  }
}

run "state_bucket_must_be_a_valid_s3_name" {
  command = plan
  variables {
    state_bucket = "-state-"
  }
  expect_failures = [var.state_bucket]
}
