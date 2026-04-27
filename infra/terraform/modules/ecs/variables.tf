variable "name" {
  description = "Name prefix for ECS resources (also the container name)"
  type        = string
}

variable "region" {
  description = "AWS region (used for awslogs config)"
  type        = string
}

variable "vpc_id" {
  description = "VPC the service runs in"
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets for tasks"
  type        = list(string)
}

variable "alb_security_group_id" {
  description = "ALB security group, allowed to reach the container port"
  type        = string
}

variable "target_group_arn" {
  description = "ALB target group to register tasks with"
  type        = string
}

variable "image" {
  description = "Container image URI (e.g. ECR repo:tag)"
  type        = string
}

variable "container_port" {
  description = "Port the relayer listens on inside the container"
  type        = number
  default     = 8080
}

variable "health_check_path" {
  description = "HTTP path used by the container-level health check"
  type        = string
  default     = "/healthz"
}

variable "task_cpu" {
  description = "Fargate task CPU units"
  type        = number
  default     = 512
}

variable "task_memory" {
  description = "Fargate task memory (MiB)"
  type        = number
  default     = 1024
}

variable "cpu_architecture" {
  description = "Fargate CPU architecture (X86_64 or ARM64)"
  type        = string
  default     = "ARM64"
}

variable "desired_count" {
  description = "Number of running tasks"
  type        = number
  default     = 2
}

variable "log_retention_days" {
  description = "CloudWatch log retention"
  type        = number
  default     = 30
}

variable "container_insights_enabled" {
  description = "Enable ECS Container Insights. Costs extra (~$0.30/metric/mo) and is NOT in the AWS free tier; default off."
  type        = bool
  default     = false
}

variable "environment" {
  description = "Plain (non-secret) env vars passed to the container"
  type        = map(string)
  default     = {}
}

variable "secrets" {
  description = "Map of env var name -> SSM Parameter / Secrets Manager ARN"
  type        = map(string)
  default     = {}
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
