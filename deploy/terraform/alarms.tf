resource "aws_sns_topic" "alerts" {
  name = "firstline-alerts"
}

resource "aws_sns_topic_subscription" "operator" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# The agent creates the same group with the same retention; the metric filter needs
# it to exist before the agent's first upload.
resource "aws_cloudwatch_log_group" "app" {
  name              = "/firstline/app"
  retention_in_days = 30
}

resource "aws_cloudwatch_log_metric_filter" "errors" {
  name           = "firstline-errors"
  log_group_name = aws_cloudwatch_log_group.app.name
  pattern        = "{ $.level = \"ERROR\" }"

  metric_transformation {
    namespace     = "firstline"
    name          = "errors"
    value         = "1"
    default_value = "0"
  }
}

locals {
  # A metric filter reports nothing for a minute without log lines, so only the
  # error alarm treats missing data as fine; a silent EMF metric means trouble.
  alarms = merge({
    errors = { namespace = "firstline", metric = "errors", stat = "Sum", op = "GreaterThanOrEqualToThreshold", threshold = 1, missing = "notBreaching" }
    queue  = { namespace = "firstline", metric = "queue_usage_avg_5m", stat = "Maximum", op = "GreaterThanThreshold", threshold = 0.5, missing = "breaching" }
    wal    = { namespace = "firstline", metric = "wal_bytes", stat = "Maximum", op = "GreaterThanThreshold", threshold = 1073741824, missing = "breaching" }
    disk   = { namespace = "CWAgent", metric = "disk_used_percent", stat = "Maximum", op = "GreaterThanThreshold", threshold = 50, missing = "breaching" }
    cert   = { namespace = "firstline", metric = "tls_cert_days_left", stat = "Minimum", op = "LessThanThreshold", threshold = 14, missing = "breaching" }
    }, var.limit_load_items_per_sec == null ? {} : {
    scale = { namespace = "firstline", metric = "sync_items_per_sec_max_5m", stat = "Maximum", op = "GreaterThanThreshold", threshold = var.limit_load_items_per_sec / 2, missing = "breaching" }
  })
}

resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "firstline-${each.key}"
  namespace           = each.value.namespace
  metric_name         = each.value.metric
  statistic           = each.value.stat
  comparison_operator = each.value.op
  threshold           = each.value.threshold
  treat_missing_data  = each.value.missing
  period              = 300
  evaluation_periods  = 1
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}
