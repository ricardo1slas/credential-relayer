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
