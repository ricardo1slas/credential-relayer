variable "name" {
  description = "Name prefix for Redis resources"
  type        = string
}

variable "vpc_id" {
  description = "VPC the cache lives in"
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets for the ElastiCache subnet group"
  type        = list(string)
}

variable "client_security_group_ids" {
  description = "Security groups (typically the ECS task SG) allowed to reach Redis on 6379"
  type        = list(string)
}

variable "node_type" {
  description = "ElastiCache instance class"
  type        = string
  default     = "cache.t4g.micro"
}

variable "engine_version" {
  description = "Redis engine version"
  type        = string
  default     = "7.1"
}

variable "num_cache_clusters" {
  description = "Number of cache nodes (>=2 enables Multi-AZ + automatic failover)"
  type        = number
  default     = 1
}

variable "transit_encryption_enabled" {
  description = "Enable in-transit encryption (TLS). When true, Redis clients must use rediss://"
  type        = bool
  default     = false
}

variable "snapshot_retention_limit" {
  description = "Days to keep automated snapshots"
  type        = number
  default     = 0
}

variable "apply_immediately" {
  description = "Apply changes without waiting for the maintenance window"
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
