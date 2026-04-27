terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.42"
    }
  }
}

locals {
  tags = merge(var.tags, { Module = "redis" })
}

resource "aws_security_group" "redis" {
  name        = "${var.name}-redis-sg"
  description = "Redis ingress restricted to ECS tasks"
  vpc_id      = var.vpc_id

  tags = merge(local.tags, { Name = "${var.name}-redis-sg" })
}

resource "aws_vpc_security_group_ingress_rule" "from_clients" {
  count                        = length(var.client_security_group_ids)
  security_group_id            = aws_security_group.redis.id
  referenced_security_group_id = var.client_security_group_ids[count.index]
  from_port                    = 6379
  to_port                      = 6379
  ip_protocol                  = "tcp"
  description                  = "Redis from client SG ${var.client_security_group_ids[count.index]}"
}

resource "aws_elasticache_subnet_group" "this" {
  name       = "${var.name}-redis-subnets"
  subnet_ids = var.private_subnet_ids
  tags       = local.tags
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id       = "${var.name}-redis"
  description                = "Relayer queue cache"
  engine                     = "redis"
  engine_version             = var.engine_version
  node_type                  = var.node_type
  port                       = 6379
  parameter_group_name       = "default.redis7"
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.redis.id]
  num_cache_clusters         = var.num_cache_clusters
  automatic_failover_enabled = var.num_cache_clusters > 1
  multi_az_enabled           = var.num_cache_clusters > 1
  at_rest_encryption_enabled = true
  transit_encryption_enabled = var.transit_encryption_enabled
  apply_immediately          = var.apply_immediately
  snapshot_retention_limit   = var.snapshot_retention_limit

  tags = merge(local.tags, { Name = "${var.name}-redis" })
}
