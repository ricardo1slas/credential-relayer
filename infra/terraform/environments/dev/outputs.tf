output "alb_dns_name" {
  description = "Public DNS for the ALB. Point a CNAME at this."
  value       = module.alb.alb_dns_name
}

output "ecs_cluster_name" {
  value = module.ecs.cluster_name
}

output "ecs_service_name" {
  value = module.ecs.service_name
}

output "task_definition_family" {
  value = module.ecs.task_definition_family
}

output "redis_endpoint" {
  value = module.redis.primary_endpoint
}

output "log_group" {
  value = module.ecs.log_group_name
}

output "ecr_repository_url" {
  description = "Push image tags here. Workflow uses this in deploy.yml."
  value       = module.ecr.repository_url
}

output "github_actions_role_arn" {
  description = "Set as AWS_DEPLOY_ROLE_ARN secret/variable in the GitHub repo."
  value       = module.github_oidc.role_arn
}

output "cloudwatch_dashboard" {
  description = "CloudWatch dashboard name."
  value       = module.cloudwatch.dashboard_name
}

output "alarm_sns_topic_arn" {
  description = "SNS topic alarms publish to. Subscribe via var.alarm_email or manually."
  value       = module.cloudwatch.sns_topic_arn
}
