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
  tags = merge(var.tags, { Module = "alb" })
}

resource "aws_security_group" "alb" {
  name        = "${var.name}-alb-sg"
  description = "Ingress for the relayer ALB"
  vpc_id      = var.vpc_id

  tags = merge(local.tags, { Name = "${var.name}-alb-sg" })
}

resource "aws_vpc_security_group_ingress_rule" "http" {
  count             = var.enable_http ? length(var.allowed_cidrs) : 0
  security_group_id = aws_security_group.alb.id
  cidr_ipv4         = var.allowed_cidrs[count.index]
  from_port         = 80
  to_port           = 80
  ip_protocol       = "tcp"
  description       = "HTTP from ${var.allowed_cidrs[count.index]}"
}

resource "aws_vpc_security_group_ingress_rule" "https" {
  count             = var.certificate_arn == "" ? 0 : length(var.allowed_cidrs)
  security_group_id = aws_security_group.alb.id
  cidr_ipv4         = var.allowed_cidrs[count.index]
  from_port         = 443
  to_port           = 443
  ip_protocol       = "tcp"
  description       = "HTTPS from ${var.allowed_cidrs[count.index]}"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.alb.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "Allow all egress (to ECS tasks in private subnets)"
}

resource "aws_lb" "this" {
  name               = "${var.name}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnet_ids

  drop_invalid_header_fields = true
  idle_timeout               = 60

  tags = merge(local.tags, { Name = "${var.name}-alb" })
}

resource "aws_lb_target_group" "this" {
  name        = "${var.name}-tg"
  port        = var.target_port
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  deregistration_delay = 30

  health_check {
    path                = var.health_check_path
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = merge(local.tags, { Name = "${var.name}-tg" })
}

resource "aws_lb_listener" "http" {
  count             = var.enable_http ? 1 : 0
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = var.certificate_arn == "" ? "forward" : "redirect"

    dynamic "forward" {
      for_each = var.certificate_arn == "" ? [1] : []
      content {
        target_group {
          arn = aws_lb_target_group.this.arn
        }
      }
    }

    dynamic "redirect" {
      for_each = var.certificate_arn == "" ? [] : [1]
      content {
        port        = "443"
        protocol    = "HTTPS"
        status_code = "HTTP_301"
      }
    }
  }
}

resource "aws_lb_listener" "https" {
  count             = var.certificate_arn == "" ? 0 : 1
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}
