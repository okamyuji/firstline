output "elastic_ip" {
  description = "Address for the DNS A record."
  value       = aws_eip.host.public_ip
}

output "instance_id" {
  description = "Target for Session Manager."
  value       = aws_instance.host.id
}

output "backup_bucket" {
  description = "Bucket for LITESTREAM_BUCKET in /etc/firstline/litestream.env."
  value       = aws_s3_bucket.backup.bucket
}

output "alert_topic" {
  description = "SNS topic the alarms publish to."
  value       = aws_sns_topic.alerts.arn
}
