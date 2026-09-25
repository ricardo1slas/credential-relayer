variable "name" {
  description = "Name prefix for all resources (e.g. relayer-dev)"
  type        = string
}

variable "region" {
  description = "AWS region (used by the dashboard widgets)"
  type        = string
}

variable "alarm_email" {
  description = "Email to subscribe to the alarm SNS topic. Empty = no subscription created."
  type        = string
  default     = ""
}

variable "alb_arn_suffix" {
  description = "ALB arn_suffix for CloudWatch LoadBalancer dimension"
  type        = string
}

variable "target_group_arn_suffix" {
  description = "Target group arn_suffix for CloudWatch TargetGroup dimension"
  type        = string
}

variable "ecs_cluster_name" {
  description = "ECS cluster name"
  type        = string
}

variable "ecs_service_name" {
  description = "ECS service name"
  type        = string
}

variable "log_group_name" {
  description = "ECS task log group name (for the error metric filter)"
  type        = string
}

variable "http_5xx_threshold" {
  description = "Alarm when target 5xx count over the period exceeds this"
  type        = number
  default     = 5
}

variable "latency_threshold_seconds" {
  description = "Alarm when target p95 response time (seconds) exceeds this"
  type        = number
  default     = 1
}

variable "cpu_threshold" {
  description = "Alarm when ECS CPU utilization (%) exceeds this"
  type        = number
  default     = 80
}

variable "memory_threshold" {
  description = "Alarm when ECS memory utilization (%) exceeds this"
  type        = number
  default     = 80
}

variable "tags" {
  description = "Tags applied to taggable resources"
  type        = map(string)
  default     = {}
}
