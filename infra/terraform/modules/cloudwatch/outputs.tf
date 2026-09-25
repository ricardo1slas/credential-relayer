output "sns_topic_arn" {
  description = "ARN of the SNS topic alarms publish to"
  value       = aws_sns_topic.alarms.arn
}

output "dashboard_name" {
  description = "CloudWatch dashboard name"
  value       = aws_cloudwatch_dashboard.this.dashboard_name
}

output "alarm_names" {
  description = "All alarm names created by this module"
  value = [
    aws_cloudwatch_metric_alarm.alb_5xx.alarm_name,
    aws_cloudwatch_metric_alarm.alb_latency.alarm_name,
    aws_cloudwatch_metric_alarm.unhealthy_hosts.alarm_name,
    aws_cloudwatch_metric_alarm.ecs_cpu.alarm_name,
    aws_cloudwatch_metric_alarm.ecs_memory.alarm_name,
    aws_cloudwatch_metric_alarm.error_logs.alarm_name,
  ]
}
