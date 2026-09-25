output "alb_arn" {
  value = aws_lb.this.arn
}

output "alb_dns_name" {
  value = aws_lb.this.dns_name
}

output "alb_zone_id" {
  value = aws_lb.this.zone_id
}

output "target_group_arn" {
  value = aws_lb_target_group.this.arn
}

# arn_suffix values are the dimension format CloudWatch expects for ALB metrics
# (e.g. "app/relayer-dev/abc123" and "targetgroup/relayer-dev/def456").
output "arn_suffix" {
  value = aws_lb.this.arn_suffix
}

output "target_group_arn_suffix" {
  value = aws_lb_target_group.this.arn_suffix
}

output "security_group_id" {
  value = aws_security_group.alb.id
}
